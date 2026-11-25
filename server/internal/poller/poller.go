package poller

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
	"github.com/letrahoo/sub-bar/server/internal/providers"
	"github.com/letrahoo/sub-bar/server/internal/store"
)

type inflightCall struct {
	done chan struct{}
	res  model.EntrySnapshot
}

type Poller struct {
	store    *store.Store
	registry *providers.Registry

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall
}

func New(st *store.Store, reg *providers.Registry) *Poller {
	return &Poller{
		store:    st,
		registry: reg,
		inflight: make(map[string]*inflightCall),
	}
}

func (p *Poller) Run(ctx context.Context) {
	// Initial sweep on startup for entries missing snapshots or overdue.
	p.sweepDue(ctx)

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepDue(ctx)
		}
	}
}

func (p *Poller) sweepDue(ctx context.Context) {
	now := time.Now().UTC()
	entries := p.store.ListEntries()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // Max 4 concurrent upstream probes

	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		snap, ok := p.store.GetSnapshot(e.ID)
		if ok && snap.NextRefreshAt != nil && now.Before(*snap.NextRefreshAt) {
			continue
		}
		entry := e
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			p.RefreshOne(ctx, entry.ID)
		}()
	}
	wg.Wait()
}

func (p *Poller) RefreshAll(ctx context.Context) model.SnapshotResponse {
	entries := p.store.ListEntries()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)

	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		id := e.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			p.RefreshOne(ctx, id)
		}()
	}
	wg.Wait()
	return p.store.BuildSnapshotResponse(p.registry.NamesMap())
}

// RefreshOne deduplicates concurrent refreshes of the same entry (singleflight).
func (p *Poller) RefreshOne(ctx context.Context, id string) (model.EntrySnapshot, bool) {
	cfg, ok := p.store.GetEntry(id)
	if !ok {
		return model.EntrySnapshot{}, false
	}

	p.inflightMu.Lock()
	if call, busy := p.inflight[id]; busy {
		p.inflightMu.Unlock()
		select {
		case <-call.done:
			return call.res, true
		case <-ctx.Done():
			prev, _ := p.store.GetSnapshot(id)
			return prev, true
		}
	}
	call := &inflightCall{done: make(chan struct{})}
	p.inflight[id] = call
	p.inflightMu.Unlock()

	var result model.EntrySnapshot
	defer func() {
		call.res = result
		close(call.done)
		p.inflightMu.Lock()
		delete(p.inflight, id)
		p.inflightMu.Unlock()
	}()

	result = p.executeProbe(ctx, cfg)
	_ = p.store.PutSnapshot(result)
	return result, true
}

func (p *Poller) executeProbe(ctx context.Context, cfg model.EntryConfig) model.EntrySnapshot {
	prov, ok := p.registry.Get(cfg.Provider)
	prev, hasPrev := p.store.GetSnapshot(cfg.ID)

	now := time.Now().UTC()
	interval := cfg.RefreshSeconds
	if interval <= 0 {
		if ok {
			interval = prov.Descriptor().DefaultRefreshSeconds
		}
		if interval <= 0 {
			interval = 300
		}
	}
	if interval < 30 {
		interval = 30
	}
	next := now.Add(time.Duration(interval) * time.Second)

	providerLabel := cfg.Provider
	defaultBadge := strings.ToUpper(cfg.Provider)
	defaultDash := ""
	if ok {
		desc := prov.Descriptor()
		providerLabel = desc.Name
		defaultBadge = desc.DefaultBadge
		defaultDash = desc.DefaultDashboardURL
	}
	badge := strings.TrimSpace(cfg.Badge)
	if badge == "" {
		badge = defaultBadge
	}
	dash := strings.TrimSpace(cfg.DashboardURL)
	if dash == "" {
		dash = defaultDash
	}

	if !cfg.Enabled {
		snap := prev
		snap.ID = cfg.ID
		snap.Name = cfg.Name
		snap.Provider = cfg.Provider
		snap.ProviderLabel = providerLabel
		snap.Kind = cfg.Kind
		snap.Badge = badge
		snap.Enabled = false
		snap.Pinned = cfg.Pinned
		snap.SortOrder = cfg.SortOrder
		snap.DashboardURL = dash
		snap.Notes = cfg.Notes
		snap.MonthlyCost = cfg.MonthlyCost
		snap.CostCurrency = cfg.CostCurrency
		snap.Status = model.StatusDisabled
		snap.StatusReason = "已停用"
		if snap.PrimaryMetric == "" {
			snap.PrimaryMetric = "已停用"
		}
		return snap
	}

	if !ok {
		return model.EntrySnapshot{
			ID:            cfg.ID,
			Name:          cfg.Name,
			Provider:      cfg.Provider,
			ProviderLabel: providerLabel,
			Kind:          cfg.Kind,
			Badge:         badge,
			Enabled:       true,
			Pinned:        cfg.Pinned,
			SortOrder:     cfg.SortOrder,
			DashboardURL:  dash,
			Status:        model.StatusError,
			StatusReason:  "未知 Provider",
			PrimaryMetric: "配置错误",
			Error:         "未知的 Provider 类型: " + cfg.Provider,
			FetchedAt:     &now,
			NextRefreshAt: &next,
		}
	}

	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	res, err := prov.Fetch(pctx, cfg)
	if res.TokenUpdate != nil {
		u := res.TokenUpdate
		_ = p.store.UpdateOAuthTokens(cfg.ID, u.AccessToken, u.RefreshToken, u.TokenExpiresAt, u.AccountEmail, u.AccountID, u.ProjectID)
	}
	if err != nil {
		// On error, backoff (5m on 429 rate-limit, 90s otherwise) and preserve last known good windows/balance as stale!
		backoff := 90 * time.Second
		if strings.Contains(err.Error(), "429") {
			backoff = 5 * time.Minute
		}
		retryAt := now.Add(backoff)
		if retryAt.After(next) && backoff < 5*time.Minute {
			retryAt = next
		}
		if hasPrev && prev.HasMetrics() {
			prev.Name = cfg.Name
			prev.Badge = badge
			prev.Pinned = cfg.Pinned
			prev.Enabled = true
			prev.SortOrder = cfg.SortOrder
			prev.AccountEmail = cfg.AccountEmail
			prev.DashboardURL = dash
			prev.Notes = cfg.Notes
			prev.MonthlyCost = cfg.MonthlyCost
			prev.CostCurrency = cfg.CostCurrency
			prev.Status = model.StatusStale
			prev.StatusReason = "暂用上次缓存"
			prev.Error = err.Error()
			prev.NextRefreshAt = &retryAt
			return prev
		}
		return model.EntrySnapshot{
			ID:            cfg.ID,
			Name:          cfg.Name,
			Provider:      cfg.Provider,
			ProviderLabel: providerLabel,
			Kind:          cfg.Kind,
			Badge:         badge,
			Enabled:       true,
			Pinned:        cfg.Pinned,
			SortOrder:     cfg.SortOrder,
			PlanLabel:     cfg.PlanLabel,
			AccountEmail:  cfg.AccountEmail,
			DashboardURL:  dash,
			Notes:         cfg.Notes,
			MonthlyCost:   cfg.MonthlyCost,
			CostCurrency:  cfg.CostCurrency,
			Status:        model.StatusError,
			StatusReason:  "拉取失败",
			PrimaryMetric: "异常",
			Error:         err.Error(),
			FetchedAt:     &now,
			NextRefreshAt: &retryAt,
		}
	}

	accountEmail := cfg.AccountEmail
	if res.AccountEmail != "" {
		accountEmail = res.AccountEmail
	}

	return model.EntrySnapshot{
		ID:            cfg.ID,
		Name:          cfg.Name,
		Provider:      cfg.Provider,
		ProviderLabel: providerLabel,
		Kind:          cfg.Kind,
		Badge:         badge,
		Enabled:       true,
		Pinned:        cfg.Pinned,
		SortOrder:     cfg.SortOrder,
		PlanLabel:     res.PlanLabel,
		AccountEmail:  accountEmail,
		DashboardURL:  dash,
		Notes:         cfg.Notes,
		MonthlyCost:   cfg.MonthlyCost,
		CostCurrency:  cfg.CostCurrency,
		Status:        res.Status,
		StatusReason:  res.StatusReason,
		PrimaryMetric: res.PrimaryMetric,
		Windows:       res.Windows,
		Balance:       res.Balance,
		Details:       res.Details,
		FetchedAt:     &now,
		NextRefreshAt: &next,
	}
}
