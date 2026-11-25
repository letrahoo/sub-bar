package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
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
		primary := filepath.Join(home, "Library", "Application Support", "SubBar")
		legacy := filepath.Join(home, "Library", "Application Support", "SubCostView")
		if _, err := os.Stat(primary); os.IsNotExist(err) {
			if info, lErr := os.Stat(legacy); lErr == nil && info.IsDir() {
				return legacy, nil
			}
		}
		return primary, nil
	}
	primary := filepath.Join(home, ".local", "share", "sub-bar")
	legacy := filepath.Join(home, ".local", "share", "sub-cost-view")
	if _, err := os.Stat(primary); os.IsNotExist(err) {
		if info, lErr := os.Stat(legacy); lErr == nil && info.IsDir() {
			return legacy, nil
		}
	}
	return primary, nil
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
	if _, changed := s.deduplicateEntriesLocked(""); changed {
		s.sortEntriesLocked()
		_ = s.saveEntriesLocked()
		_ = s.saveSnapshotsLocked()
	}
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
	if matchIdx := s.findDuplicateIndexLocked(cfg, ""); matchIdx >= 0 {
		merged := mergeIncomingIntoExisting(s.entries[matchIdx], cfg, now)
		s.entries[matchIdx] = merged
		s.syncSnapshotMetadataLocked(merged)
		s.sortEntriesLocked()
		if err := s.saveEntriesLocked(); err != nil {
			return model.EntryConfig{}, err
		}
		return merged, nil
	}

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
	s.syncSnapshotMetadataLocked(incoming)

	survivorID := incoming.ID
	if sid, changed := s.deduplicateEntriesLocked(incoming.ID); changed && sid != "" {
		survivorID = sid
	}
	s.sortEntriesLocked()

	if err := s.saveEntriesLocked(); err != nil {
		return model.EntryConfig{}, err
	}
	_ = s.saveSnapshotsLocked()

	for _, e := range s.entries {
		if e.ID == survivorID {
			return e, nil
		}
	}
	return incoming, nil
}

func (s *Store) syncSnapshotMetadataLocked(incoming model.EntryConfig) {
	if snap, ok := s.snapshots[incoming.ID]; ok {
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
		s.snapshots[incoming.ID] = snap
	}
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
	if _, changed := s.deduplicateEntriesLocked(cur.ID); changed {
		s.sortEntriesLocked()
		_ = s.saveSnapshotsLocked()
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

		hasData := snap.HasMetrics()
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

// ============================================================================
// Same-Account + Same-Source Deduplication
// ============================================================================

func (s *Store) findDuplicateIndexLocked(candidate model.EntryConfig, excludeID string) int {
	for i, existing := range s.entries {
		if excludeID != "" && existing.ID == excludeID {
			continue
		}
		if entriesSameAccountSource(existing, candidate) {
			return i
		}
	}
	return -1
}

// FindDuplicate returns an existing entry matching candidate's provider/source and account identity.
func (s *Store) FindDuplicate(candidate model.EntryConfig) (model.EntryConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if idx := s.findDuplicateIndexLocked(candidate, ""); idx >= 0 {
		return s.entries[idx], true
	}
	return model.EntryConfig{}, false
}

// deduplicateEntriesLocked merges entries that share the same provider/source and account identity.
// If focusID is non-empty and participates in a duplicate group, its surviving ID is returned.
func (s *Store) deduplicateEntriesLocked(focusID string) (survivorForFocus string, changed bool) {
	if len(s.entries) < 2 {
		return focusID, false
	}
	survivorForFocus = focusID
	kept := make([]model.EntryConfig, 0, len(s.entries))

	for _, entry := range s.entries {
		matchIdx := -1
		for i, k := range kept {
			if entriesSameAccountSource(k, entry) {
				matchIdx = i
				break
			}
		}
		if matchIdx < 0 {
			kept = append(kept, entry)
			continue
		}

		changed = true
		existing := kept[matchIdx]
		winner, loser := pickPrimaryDuplicate(existing, entry, s.snapshots)
		merged := mergeDuplicateConfigs(winner, loser, s.snapshots)
		kept[matchIdx] = merged

		if focusID != "" && (loser.ID == focusID || winner.ID == focusID) {
			survivorForFocus = merged.ID
		}
		// Preserve valid snapshot if winner lacked one, then remove loser snapshot.
		wSnap, wOK := s.snapshots[merged.ID]
		lSnap, lOK := s.snapshots[loser.ID]
		if lOK && (!wOK || (!wSnap.HasMetrics() && lSnap.HasMetrics()) || (wSnap.Status == model.StatusError && lSnap.Status != model.StatusError && lSnap.HasMetrics())) {
			lSnap.ID = merged.ID
			s.snapshots[merged.ID] = lSnap
		}
		delete(s.snapshots, loser.ID)
		s.syncSnapshotMetadataLocked(merged)
	}

	if changed {
		s.entries = kept
	}
	return survivorForFocus, changed
}

func entriesSameAccountSource(a, b model.EntryConfig) bool {
	provA := strings.ToLower(strings.TrimSpace(a.Provider))
	provB := strings.ToLower(strings.TrimSpace(b.Provider))
	if provA == "" || provA != provB {
		return false
	}
	// Independent manual ledgers and generic HTTP extractors can legitimately have multiple entries.
	if provA == "manual" || provA == "custom_http" {
		return false
	}
	if normalizeBaseURLForDedup(provA, a.BaseURL) != normalizeBaseURLForDedup(provB, b.BaseURL) {
		return false
	}

	switch provA {
	case "codex_oauth":
		emailA := strings.ToLower(strings.TrimSpace(a.AccountEmail))
		emailB := strings.ToLower(strings.TrimSpace(b.AccountEmail))
		wsA := strings.ToLower(strings.TrimSpace(a.AccountID))
		wsB := strings.ToLower(strings.TrimSpace(b.AccountID))
		if emailA != "" && emailB != "" {
			if emailA != emailB {
				return false
			}
			// Preserve distinct ChatGPT workspaces under the same email when both specify workspace IDs.
			if wsA != "" && wsB != "" && wsA != wsB {
				return false
			}
			return true
		}
		if wsA != "" && wsB != "" && wsA == wsB {
			return true
		}
		return sameNonEmptyCredential(a, b)

	case "antigravity_oauth", "claude_oauth", "kimi_oauth":
		emailA := strings.ToLower(strings.TrimSpace(a.AccountEmail))
		emailB := strings.ToLower(strings.TrimSpace(b.AccountEmail))
		if emailA != "" && emailB != "" {
			return emailA == emailB
		}
		return sameNonEmptyCredential(a, b)

	default:
		// API-key providers (gemini_api, deepseek, openrouter, moonshot, siliconflow, openai_billing):
		// deduplicate only when the API key itself matches on the same source/endpoint.
		secA := strings.TrimSpace(a.Secret)
		secB := strings.TrimSpace(b.Secret)
		return secA != "" && secA == secB
	}
}

func sameNonEmptyCredential(a, b model.EntryConfig) bool {
	rtA := strings.TrimSpace(a.RefreshToken)
	rtB := strings.TrimSpace(b.RefreshToken)
	if rtA != "" && rtA == rtB {
		return true
	}
	secA := strings.TrimSpace(a.Secret)
	secB := strings.TrimSpace(b.Secret)
	return secA != "" && secA == secB
}

func normalizeBaseURLForDedup(provider, raw string) string {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s != "" {
		if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
			u.Scheme = strings.ToLower(u.Scheme)
			u.Host = strings.ToLower(u.Host)
			u.Path = strings.TrimRight(u.Path, "/")
			s = u.String()
		}
	}
	lower := strings.ToLower(s)

	switch provider {
	case "gemini_api":
		if s == "" || lower == "https://generativelanguage.googleapis.com" || lower == "https://generativelanguage.googleapis.com/v1beta" {
			return "https://generativelanguage.googleapis.com/v1beta"
		}
		if !strings.HasSuffix(lower, "/v1beta") && !strings.HasSuffix(lower, "/v1") && !strings.HasSuffix(lower, "/v1alpha") {
			return s + "/v1beta"
		}
		return s
	case "kimi_oauth":
		if s == "" || lower == "https://api.kimi.com" || lower == "https://www.kimi.com" {
			return "https://api.kimi.com"
		}
		if lower == "https://api.kimi.ai" || lower == "https://www.kimi.ai" {
			return "https://api.kimi.ai"
		}
		return s
	case "deepseek":
		if s == "" || lower == "https://api.deepseek.com/user/balance" {
			return "https://api.deepseek.com/user/balance"
		}
		return s
	case "openrouter":
		if s == "" || lower == "https://openrouter.ai/api/v1" {
			return "https://openrouter.ai/api/v1"
		}
		return s
	case "moonshot":
		if s == "" || lower == "https://api.moonshot.cn/v1/users/me/balance" {
			return "https://api.moonshot.cn/v1/users/me/balance"
		}
		return s
	case "siliconflow":
		if s == "" || lower == "https://api.siliconflow.cn/v1/user/info" {
			return "https://api.siliconflow.cn/v1/user/info"
		}
		return s
	case "openai_billing":
		if strings.HasSuffix(lower, "/v1") {
			return s[:len(s)-3]
		}
		return s
	default:
		return s
	}
}

func pickPrimaryDuplicate(a, b model.EntryConfig, snaps map[string]model.EntrySnapshot) (winner, loser model.EntryConfig) {
	if duplicatePriorityScore(b, snaps) > duplicatePriorityScore(a, snaps) {
		return b, a
	}
	if duplicatePriorityScore(a, snaps) > duplicatePriorityScore(b, snaps) {
		return a, b
	}
	if a.SortOrder != b.SortOrder {
		if a.SortOrder < b.SortOrder {
			return a, b
		}
		return b, a
	}
	if b.CreatedAt.Before(a.CreatedAt) {
		return b, a
	}
	return a, b
}

func duplicatePriorityScore(e model.EntryConfig, snaps map[string]model.EntrySnapshot) int {
	score := 0
	if e.Enabled {
		score += 100
	}
	if e.Pinned {
		score += 40
	}
	if snap, ok := snaps[e.ID]; ok && snap.HasMetrics() && snap.Status != model.StatusError {
		score += 20
	}
	if strings.TrimSpace(e.RefreshToken) != "" {
		score += 10
	}
	if strings.TrimSpace(e.AccountEmail) != "" {
		score += 5
	}
	if e.MonthlyCost != nil && *e.MonthlyCost > 0 {
		score += 2
	}
	return score
}

func mergeDuplicateConfigs(winner, loser model.EntryConfig, snaps map[string]model.EntrySnapshot) model.EntryConfig {
	out := winner
	out.Pinned = winner.Pinned || loser.Pinned
	out.Enabled = winner.Enabled || loser.Enabled

	// Prefer the fresher valid credentials when loser was updated more recently.
	loserSnap, loserHasSnap := snaps[loser.ID]
	loserHealthy := !loserHasSnap || loserSnap.Status != model.StatusError
	if loser.UpdatedAt.After(winner.UpdatedAt) && loserHealthy {
		if strings.TrimSpace(loser.Secret) != "" {
			out.Secret = loser.Secret
		}
		if strings.TrimSpace(loser.RefreshToken) != "" {
			out.RefreshToken = loser.RefreshToken
		}
		if loser.TokenExpiresAt != nil {
			out.TokenExpiresAt = loser.TokenExpiresAt
		}
	} else {
		if strings.TrimSpace(out.Secret) == "" {
			out.Secret = loser.Secret
		}
		if strings.TrimSpace(out.RefreshToken) == "" {
			out.RefreshToken = loser.RefreshToken
		}
		if out.TokenExpiresAt == nil {
			out.TokenExpiresAt = loser.TokenExpiresAt
		}
	}

	out.AccountEmail = coalesce(out.AccountEmail, loser.AccountEmail)
	out.AccountID = coalesce(out.AccountID, loser.AccountID)
	out.ProjectID = coalesce(out.ProjectID, loser.ProjectID)
	out.PlanLabel = coalesce(out.PlanLabel, loser.PlanLabel)
	out.DashboardURL = coalesce(out.DashboardURL, loser.DashboardURL)
	out.Notes = coalesce(out.Notes, loser.Notes)
	out.ProbeMode = coalesce(out.ProbeMode, loser.ProbeMode)
	out.ProbeModel = coalesce(out.ProbeModel, loser.ProbeModel)
	if out.MonthlyCost == nil {
		out.MonthlyCost = loser.MonthlyCost
	}
	out.CostCurrency = coalesce(out.CostCurrency, loser.CostCurrency)
	if loser.UpdatedAt.After(out.UpdatedAt) {
		out.UpdatedAt = loser.UpdatedAt
	}
	return out
}

func mergeIncomingIntoExisting(existing, incoming model.EntryConfig, now time.Time) model.EntryConfig {
	out := existing
	out.UpdatedAt = now
	if strings.TrimSpace(incoming.Name) != "" {
		out.Name = strings.TrimSpace(incoming.Name)
	}
	if strings.TrimSpace(incoming.Badge) != "" {
		out.Badge = strings.TrimSpace(incoming.Badge)
	}
	out.Enabled = incoming.Enabled
	out.Pinned = existing.Pinned || incoming.Pinned
	if incoming.RefreshSeconds >= 30 {
		out.RefreshSeconds = incoming.RefreshSeconds
	}
	if incoming.MonthlyCost != nil {
		out.MonthlyCost = incoming.MonthlyCost
	}
	out.CostCurrency = coalesce(incoming.CostCurrency, existing.CostCurrency)
	out.DashboardURL = coalesce(incoming.DashboardURL, existing.DashboardURL)
	out.Notes = coalesce(incoming.Notes, existing.Notes)
	out.PlanLabel = coalesce(incoming.PlanLabel, existing.PlanLabel)
	out.BaseURL = coalesce(incoming.BaseURL, existing.BaseURL)
	out.ProbeMode = coalesce(incoming.ProbeMode, existing.ProbeMode)
	out.ProbeModel = coalesce(incoming.ProbeModel, existing.ProbeModel)
	out.Currency = coalesce(incoming.Currency, existing.Currency)
	if incoming.LowThreshold != nil {
		out.LowThreshold = incoming.LowThreshold
	}
	if incoming.WarnPercent != nil {
		out.WarnPercent = incoming.WarnPercent
	}

	// Update credentials from incoming while keeping existing refresh token if incoming omitted it.
	out.AuthMode = coalesce(incoming.AuthMode, existing.AuthMode)
	if strings.TrimSpace(incoming.Secret) != "" {
		out.Secret = strings.TrimSpace(incoming.Secret)
	}
	if strings.TrimSpace(incoming.RefreshToken) != "" {
		out.RefreshToken = strings.TrimSpace(incoming.RefreshToken)
	}
	if incoming.TokenExpiresAt != nil {
		out.TokenExpiresAt = incoming.TokenExpiresAt
	}
	out.AccountEmail = coalesce(incoming.AccountEmail, existing.AccountEmail)
	out.AccountID = coalesce(incoming.AccountID, existing.AccountID)
	out.ProjectID = coalesce(incoming.ProjectID, existing.ProjectID)
	return out
}
