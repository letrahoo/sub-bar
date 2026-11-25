package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
)

func TestStoreOpenDeduplicatesLegacyDuplicatesOnDisk(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

	seedEntries := persistedEntries{
		Version: 1,
		Entries: []model.EntryConfig{
			{
				ID:           "agy-pinned",
				Name:         "Antigravity (letrahu99)",
				Provider:     "antigravity_oauth",
				Kind:         model.KindSubscription,
				Enabled:      true,
				Pinned:       true,
				SortOrder:    10,
				AccountEmail: "letrahu99@gmail.com",
				Secret:       "ya29.older",
				RefreshToken: "1//older",
				CreatedAt:    t1,
				UpdatedAt:    t1,
			},
			{
				ID:           "agy-dup",
				Name:         "Antigravity (letrahu99)",
				Provider:     "antigravity_oauth",
				Kind:         model.KindSubscription,
				Enabled:      true,
				Pinned:       false,
				SortOrder:    50,
				AccountEmail: "LETRAHU99@gmail.com",
				Secret:       "ya29.fresher",
				RefreshToken: "1//fresher",
				Notes:        "backup note",
				CreatedAt:    t2,
				UpdatedAt:    t2,
			},
			{
				ID:           "agy-other",
				Name:         "Antigravity (work)",
				Provider:     "antigravity_oauth",
				Kind:         model.KindSubscription,
				Enabled:      true,
				Pinned:       true,
				SortOrder:    20,
				AccountEmail: "work@gmail.com",
				Secret:       "ya29.work",
				CreatedAt:    t1,
				UpdatedAt:    t1,
			},
			{
				ID:           "codex-ws1",
				Name:         "Codex Personal",
				Provider:     "codex_oauth",
				Kind:         model.KindSubscription,
				Enabled:      true,
				SortOrder:    30,
				AccountEmail: "same@example.com",
				AccountID:    "ws-personal",
				Secret:       "jwt-1",
				CreatedAt:    t1,
				UpdatedAt:    t1,
			},
			{
				ID:           "codex-ws2",
				Name:         "Codex Team",
				Provider:     "codex_oauth",
				Kind:         model.KindSubscription,
				Enabled:      true,
				SortOrder:    40,
				AccountEmail: "same@example.com",
				AccountID:    "ws-team",
				Secret:       "jwt-2",
				CreatedAt:    t2,
				UpdatedAt:    t2,
			},
			{
				ID:        "manual-1",
				Name:      "Cursor Pro",
				Provider:  "manual",
				Kind:      model.KindSubscription,
				Enabled:   true,
				SortOrder: 60,
				CreatedAt: t1,
				UpdatedAt: t1,
			},
			{
				ID:        "manual-2",
				Name:      "Copilot",
				Provider:  "manual",
				Kind:      model.KindSubscription,
				Enabled:   true,
				SortOrder: 70,
				CreatedAt: t2,
				UpdatedAt: t2,
			},
		},
	}
	raw, err := json.Marshal(seedEntries)
	if err != nil {
		t.Fatalf("marshal seed entries: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entries.json"), raw, 0o600); err != nil {
		t.Fatalf("write seed entries: %v", err)
	}

	seedSnaps := persistedSnapshots{
		Version: 1,
		Snapshots: map[string]model.EntrySnapshot{
			"agy-pinned": {ID: "agy-pinned", Status: model.StatusOK, PrimaryMetric: "剩余 95%"},
			"agy-dup":    {ID: "agy-dup", Status: model.StatusOK, PrimaryMetric: "剩余 95%"},
		},
	}
	rawSnaps, _ := json.Marshal(seedSnaps)
	if err := os.WriteFile(filepath.Join(dir, "snapshots.json"), rawSnaps, 0o600); err != nil {
		t.Fatalf("write seed snaps: %v", err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	entries := st.ListEntries()
	if len(entries) != 6 {
		t.Fatalf("expected 6 deduplicated entries (7 - 1 agy dup), got %d: %+v", len(entries), entries)
	}

	agy, ok := st.GetEntry("agy-pinned")
	if !ok {
		t.Fatalf("expected pinned agy entry to survive")
	}
	if _, stillThere := st.GetEntry("agy-dup"); stillThere {
		t.Fatalf("expected agy-dup to be removed")
	}
	if agy.Secret != "ya29.fresher" || agy.RefreshToken != "1//fresher" || agy.Notes != "backup note" {
		t.Fatalf("expected fresher tokens & backfilled metadata preserved on winner, got %+v", agy)
	}
	if _, dupSnap := st.GetSnapshot("agy-dup"); dupSnap {
		t.Fatalf("expected duplicate snapshot cleaned up")
	}

	// Re-open from disk to confirm deduplicated state was persisted.
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	if len(st2.ListEntries()) != 6 {
		t.Fatalf("expected persisted deduplicated entries (6), got %d", len(st2.ListEntries()))
	}
}

func TestStoreAddAndUpdateDeduplicatesSameAccountSource(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// 1. Same Gemini API Key with equivalent default / explicit BaseURL merges in-place
	g1, err := st.AddEntry(model.EntryConfig{
		Name:      "Gemini Key 1",
		Provider:  "gemini_api",
		Kind:      model.KindBalance,
		Enabled:   true,
		Pinned:    true,
		Secret:    "AIzaSameKey",
		ProbeMode: "models",
	})
	if err != nil {
		t.Fatalf("add g1: %v", err)
	}
	g2, err := st.AddEntry(model.EntryConfig{
		Name:      "Gemini Key Updated",
		Provider:  "gemini_api",
		Kind:      model.KindBalance,
		Enabled:   true,
		BaseURL:   "https://generativelanguage.googleapis.com/v1beta/",
		Secret:    "AIzaSameKey",
		ProbeMode: "generate",
	})
	if err != nil {
		t.Fatalf("add g2: %v", err)
	}
	if g2.ID != g1.ID || !g2.Pinned || g2.ProbeMode != "generate" {
		t.Fatalf("expected same Gemini key merged into existing entry %s, got %+v", g1.ID, g2)
	}

	// Different Gemini API Key stays distinct
	g3, err := st.AddEntry(model.EntryConfig{
		Name:     "Gemini Key 2",
		Provider: "gemini_api",
		Kind:     model.KindBalance,
		Enabled:  true,
		Secret:   "AIzaDifferentKey",
	})
	if err != nil {
		t.Fatalf("add g3: %v", err)
	}
	if g3.ID == g1.ID {
		t.Fatalf("distinct Gemini API key must not merge with g1")
	}

	// 2. Same OpenAI Billing key on DIFFERENT relay hosts stay distinct; same host (/v1 vs root) merges
	r1, _ := st.AddEntry(model.EntryConfig{
		Name:     "Relay A",
		Provider: "openai_billing",
		Kind:     model.KindBalance,
		Enabled:  true,
		BaseURL:  "https://relay-a.example.com",
		Secret:   "sk-relay",
	})
	r2, _ := st.AddEntry(model.EntryConfig{
		Name:     "Relay B",
		Provider: "openai_billing",
		Kind:     model.KindBalance,
		Enabled:  true,
		BaseURL:  "https://relay-b.example.com",
		Secret:   "sk-relay",
	})
	if r1.ID == r2.ID {
		t.Fatalf("different relay hosts must remain separate entries")
	}
	r3, _ := st.AddEntry(model.EntryConfig{
		Name:     "Relay A v1",
		Provider: "openai_billing",
		Kind:     model.KindBalance,
		Enabled:  true,
		BaseURL:  "https://relay-a.example.com/v1/",
		Secret:   "sk-relay",
	})
	if r3.ID != r1.ID {
		t.Fatalf("same relay host with /v1 suffix should merge into %s, got %s", r1.ID, r3.ID)
	}

	// 3. Kimi OAuth: different random device AccountIDs merge once AccountEmail resolves via UpdateEntry
	k1, _ := st.AddEntry(model.EntryConfig{
		Name:         "Kimi (Alice)",
		Provider:     "kimi_oauth",
		Kind:         model.KindSubscription,
		Enabled:      true,
		Pinned:       true,
		AccountEmail: "Alice",
		AccountID:    "device-uuid-1",
		Secret:       "kimi-at-1",
		RefreshToken: "kimi-rt-1",
	})
	k2, _ := st.AddEntry(model.EntryConfig{
		Name:         "Kimi 订阅",
		Provider:     "kimi_oauth",
		Kind:         model.KindSubscription,
		Enabled:      true,
		AccountID:    "device-uuid-2",
		Secret:       "kimi-at-2",
		RefreshToken: "kimi-rt-2",
	})
	if k2.ID == k1.ID {
		t.Fatalf("unidentified Kimi entry should be separate until account identity resolves")
	}
	k2.AccountEmail = "alice"
	mergedKimi, err := st.UpdateEntry(k2.ID, k2, true)
	if err != nil {
		t.Fatalf("update k2 with resolved email: %v", err)
	}
	if mergedKimi.ID != k1.ID {
		t.Fatalf("expected resolved Kimi duplicate to merge into pinned %s, got %s", k1.ID, mergedKimi.ID)
	}
	if mergedKimi.Secret != "kimi-at-2" {
		t.Fatalf("expected fresher token adopted on merge, got %q", mergedKimi.Secret)
	}
	if _, exists := st.GetEntry(k2.ID); exists {
		t.Fatalf("expected merged duplicate %s to be deleted", k2.ID)
	}
}
