package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"xin.letra/sub-cost-view/server/internal/model"
)

type persistedEntries struct {
	Version int                 `json:"version"`
	Entries []model.EntryConfig `json:"entries"`
}

type persistedSnapshots struct {
	Version   int                            `json:"version"`
	Snapshots map[string]model.EntrySnapshot `json:"snapshots"`
}

type Store struct {
	mu        sync.RWMutex
	dir       string
	entries   []model.EntryConfig
	snapshots map[string]model.EntrySnapshot
}

func DefaultDataDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("SUBBAR_DATA_DIR")); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "SubCostView"), nil
	}
	return filepath.Join(home, ".local", "share", "sub-cost-view"), nil
}

func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	_ = os.Chmod(dir, 0o700)

	s := &Store{
		dir:       dir,
		entries:   []model.EntryConfig{},
		snapshots: make(map[string]model.EntrySnapshot),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) entriesPath() string   { return filepath.Join(s.dir, "entries.json") }
func (s *Store) snapshotsPath() string { return filepath.Join(s.dir, "snapshots.json") }

func (s *Store) load() error {
	if raw, err := os.ReadFile(s.entriesPath()); err == nil && len(raw) > 0 {
		var pe persistedEntries
		if err := json.Unmarshal(raw, &pe); err != nil {
			return fmt.Errorf("parse entries.json: %w", err)
		}
		s.entries = pe.Entries
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read entries.json: %w", err)
	}

	if raw, err := os.ReadFile(s.snapshotsPath()); err == nil && len(raw) > 0 {
		var ps persistedSnapshots
		if err := json.Unmarshal(raw, &ps); err == nil && ps.Snapshots != nil {
			s.snapshots = ps.Snapshots
		}
	}
	s.sortEntriesLocked()
	return nil
}

func (s *Store) sortEntriesLocked() {
	sort.SliceStable(s.entries, func(i, j int) bool {
		if s.entries[i].Pinned != s.entries[j].Pinned {
			return s.entries[i].Pinned
		}
		if s.entries[i].SortOrder != s.entries[j].SortOrder {
			return s.entries[i].SortOrder < s.entries[j].SortOrder
		}
		return s.entries[i].CreatedAt.Before(s.entries[j].CreatedAt)
	})
}

func writeAtomicJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) saveEntriesLocked() error {
	return writeAtomicJSON(s.entriesPath(), persistedEntries{
		Version: 1,
		Entries: s.entries,
	})
}

func (s *Store) saveSnapshotsLocked() error {
	return writeAtomicJSON(s.snapshotsPath(), persistedSnapshots{
		Version:   1,
		Snapshots: s.snapshots,
	})
}

func (s *Store) ListEntries() []model.EntryConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.EntryConfig, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *Store) ListPublicEntries() []model.EntryConfigPublic {
	entries := s.ListEntries()
	out := make([]model.EntryConfigPublic, len(entries))
	for i, e := range entries {
		out[i] = e.Public()
	}
	return out
}

func (s *Store) GetEntry(id string) (model.EntryConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return model.EntryConfig{}, false
}

func (s *Store) GetSnapshot(id string) (model.EntrySnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snapshots[id]
	return snap, ok
}

func (s *Store) PutSnapshot(snap model.EntrySnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Ensure entry still exists before saving snapshot.
	exists := false
	for _, e := range s.entries {
		if e.ID == snap.ID {
			exists = true
			snap.Name = e.Name
			snap.Pinned = e.Pinned
			snap.Enabled = e.Enabled
			snap.SortOrder = e.SortOrder
			snap.Badge = coalesce(e.Badge, snap.Badge)
			snap.AccountEmail = coalesce(e.AccountEmail, snap.AccountEmail)
			snap.DashboardURL = coalesce(e.DashboardURL, snap.DashboardURL)
			snap.Notes = e.Notes
			snap.MonthlyCost = e.MonthlyCost
			snap.CostCurrency = e.CostCurrency
			break
		}
	}
	if !exists {
		return nil
	}
	s.snapshots[snap.ID] = snap
	return s.saveSnapshotsLocked()
}

func (s *Store) AddEntry(cfg model.EntryConfig) (model.EntryConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if strings.TrimSpace(cfg.ID) == "" {
		cfg.ID = newID(cfg.Provider)
	}
	for _, existing := range s.entries {
		if existing.ID == cfg.ID {
			cfg.ID = newID(cfg.Provider)
			break
		}
	}
	if cfg.SortOrder == 0 {
		cfg.SortOrder = (len(s.entries) + 1) * 10
	}
	cfg.CreatedAt = now
	cfg.UpdatedAt = now
	s.entries = append(s.entries, cfg)
	s.sortEntriesLocked()
	if err := s.saveEntriesLocked(); err != nil {
		return model.EntryConfig{}, err
	}
	return cfg, nil
}

func (s *Store) UpdateEntry(id string, incoming model.EntryConfig, keepSecret bool) (model.EntryConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, e := range s.entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return model.EntryConfig{}, os.ErrNotExist
	}
	prev := s.entries[idx]
	incoming.ID = prev.ID
	incoming.CreatedAt = prev.CreatedAt
	incoming.UpdatedAt = time.Now().UTC()
	if keepSecret {
		if incoming.Secret == "" {
			incoming.Secret = prev.Secret
		}
		if incoming.RefreshToken == "" {
			incoming.RefreshToken = prev.RefreshToken
		}
		if incoming.TokenExpiresAt == nil {
			incoming.TokenExpiresAt = prev.TokenExpiresAt
		}
		if incoming.AccountEmail == "" {
			incoming.AccountEmail = prev.AccountEmail
		}
		if incoming.ProjectID == "" {
			incoming.ProjectID = prev.ProjectID
		}
		if incoming.AccountID == "" {
			incoming.AccountID = prev.AccountID
		}
		if incoming.AuthMode == "" {
			incoming.AuthMode = prev.AuthMode
		}
		if incoming.CredentialFile == "" {
			incoming.CredentialFile = prev.CredentialFile
		}
	}
	if incoming.SortOrder == 0 {
		incoming.SortOrder = prev.SortOrder
	}
	s.entries[idx] = incoming
	s.sortEntriesLocked()

	// Sync metadata fields onto existing cached snapshot immediately.
	if snap, ok := s.snapshots[id]; ok {
		snap.Name = incoming.Name
		snap.Pinned = incoming.Pinned
		snap.Enabled = incoming.Enabled
		snap.SortOrder = incoming.SortOrder
		snap.Kind = incoming.Kind
		if incoming.Badge != "" {
			snap.Badge = incoming.Badge
		}
		snap.AccountEmail = incoming.AccountEmail
		snap.DashboardURL = incoming.DashboardURL
		snap.Notes = incoming.Notes
		snap.MonthlyCost = incoming.MonthlyCost
		snap.CostCurrency = incoming.CostCurrency
		if !incoming.Enabled {
			snap.Status = model.StatusDisabled
			snap.StatusReason = "已停用"
		}
		s.snapshots[id] = snap
		_ = s.saveSnapshotsLocked()
	}

	if err := s.saveEntriesLocked(); err != nil {
		return model.EntryConfig{}, err
	}
	return incoming, nil
}

// UpdateOAuthTokens persists silently refreshed OAuth credentials without overwriting user metadata.
func (s *Store) UpdateOAuthTokens(id, accessToken, refreshToken string, expiresAt *time.Time, accountEmail, accountID, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, e := range s.entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return os.ErrNotExist
	}
	cur := s.entries[idx]
	if accessToken != "" {
		cur.Secret = accessToken
	}
	if refreshToken != "" {
		cur.RefreshToken = refreshToken
	}
	if expiresAt != nil {
		cur.TokenExpiresAt = expiresAt
	}
	if accountEmail != "" {
		cur.AccountEmail = accountEmail
	}
	if accountID != "" {
		cur.AccountID = accountID
	}
	if projectID != "" {
		cur.ProjectID = projectID
	}
	if cur.AuthMode == "" {
		cur.AuthMode = "oauth"
	}
	cur.UpdatedAt = time.Now().UTC()
	s.entries[idx] = cur
	if snap, ok := s.snapshots[id]; ok && cur.AccountEmail != "" {
		snap.AccountEmail = cur.AccountEmail
		s.snapshots[id] = snap
	}
	return s.saveEntriesLocked()
}

func (s *Store) DeleteEntry(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]model.EntryConfig, 0, len(s.entries))
	found := false
	for _, e := range s.entries {
		if e.ID == id {
			found = true
			continue
		}
		next = append(next, e)
	}
	if !found {
		return os.ErrNotExist
	}
	s.entries = next
	delete(s.snapshots, id)
	if err := s.saveEntriesLocked(); err != nil {
		return err
	}
	_ = s.saveSnapshotsLocked()
	return nil
}

func (s *Store) BuildSnapshotResponse(providerNames map[string]string) model.SnapshotResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]model.EntrySnapshot, 0, len(s.entries))
	balanceTotals := make(map[string]float64)
	monthlyTotals := make(map[string]float64)

	var summary model.SummaryStats
	summary.TotalEntries = len(s.entries)

	for _, e := range s.entries {
		if e.Enabled {
			summary.EnabledEntries++
		}
		if e.Kind == model.KindBalance {
			summary.BalanceCount++
		} else {
			summary.SubscriptionCount++
		}
		if e.Enabled && e.MonthlyCost != nil && *e.MonthlyCost > 0 {
			cur := strings.ToUpper(strings.TrimSpace(e.CostCurrency))
			if cur == "" {
				cur = strings.ToUpper(strings.TrimSpace(e.Currency))
			}
			if cur == "" {
				cur = "USD"
			}
			monthlyTotals[cur] += *e.MonthlyCost
		}

		snap, ok := s.snapshots[e.ID]
		if !ok {
			st := model.StatusOK
			reason := "等待首次刷新"
			if !e.Enabled {
				st = model.StatusDisabled
				reason = "已停用"
			}
			snap = model.EntrySnapshot{
				ID:            e.ID,
				Name:          e.Name,
				Provider:      e.Provider,
				ProviderLabel: providerNames[e.Provider],
				Kind:          e.Kind,
				Badge:         coalesce(e.Badge, strings.ToUpper(takeRunes(e.Provider, 3))),
				Enabled:       e.Enabled,
				Pinned:        e.Pinned,
				SortOrder:     e.SortOrder,
				PlanLabel:     e.PlanLabel,
				AccountEmail:  e.AccountEmail,
				DashboardURL:  e.DashboardURL,
				Notes:         e.Notes,
				MonthlyCost:   e.MonthlyCost,
				CostCurrency:  e.CostCurrency,
				Status:        st,
				StatusReason:  reason,
				PrimaryMetric: "加载中…",
			}
		} else {
			snap.Name = e.Name
			snap.Pinned = e.Pinned
			snap.Enabled = e.Enabled
			snap.SortOrder = e.SortOrder
			if e.Badge != "" {
				snap.Badge = e.Badge
			}
			if e.AccountEmail != "" {
				snap.AccountEmail = e.AccountEmail
			}
			if !e.Enabled {
				snap.Status = model.StatusDisabled
				snap.StatusReason = "已停用"
			}
		}

		hasData := len(snap.Windows) > 0 || snap.Balance != nil
		if e.Enabled && hasData {
			switch snap.Status {
			case model.StatusWarning, model.StatusStale:
				summary.WarningCount++
			case model.StatusCritical, model.StatusError:
				summary.CriticalCount++
			}
			if snap.Balance != nil && !math.IsNaN(snap.Balance.Available) && !math.IsInf(snap.Balance.Available, 0) {
				cur := strings.ToUpper(strings.TrimSpace(snap.Balance.Currency))
				if cur == "" {
					cur = "USD"
				}
				balanceTotals[cur] += snap.Balance.Available
			}
		}
		list = append(list, snap)
	}

	summary.AvailableBalances = sortedCurrencyTotals(balanceTotals)
	summary.MonthlyFixedCosts = sortedCurrencyTotals(monthlyTotals)

	return model.SnapshotResponse{
		GeneratedAt: time.Now().UTC(),
		Summary:     summary,
		Entries:     list,
	}
}

func sortedCurrencyTotals(m map[string]float64) []model.CurrencyTotal {
	if len(m) == 0 {
		return []model.CurrencyTotal{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		// Put CNY and USD first in deterministic order.
		rank := func(c string) int {
			switch c {
			case "CNY":
				return 0
			case "USD":
				return 1
			default:
				return 2
			}
		}
		ri, rj := rank(keys[i]), rank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	out := make([]model.CurrencyTotal, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.CurrencyTotal{
			Currency: k,
			Symbol:   model.CurrencySymbol(k),
			Amount:   math.Round(m[k]*100) / 100,
		})
	}
	return out
}

func newID(provider string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return '-'
	}, provider)
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "sub"
	}
	if len(clean) > 10 {
		clean = clean[:10]
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return clean + "-" + hex.EncodeToString(b[:])
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func takeRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
