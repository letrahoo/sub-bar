package poller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
	"github.com/letrahoo/sub-bar/server/internal/providers"
	"github.com/letrahoo/sub-bar/server/internal/store"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonHTTPResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestPollerSingleflightDisabledAndStaleFallback(t *testing.T) {
	var calls int32
	var failUpstream atomic.Bool

	restore := providers.SetHTTPTransportForTest(rtFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(40 * time.Millisecond)
		if failUpstream.Load() {
			return jsonHTTPResp(503, `{"error":"temporary outage"}`), nil
		}
		return jsonHTTPResp(200, `{
			"models": [
				{"name": "models/gemini-2.5-flash", "supportedGenerationMethods": ["generateContent"]}
			]
		}`), nil
	}))
	defer restore()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	reg := providers.NewRegistry()
	pl := New(st, reg)

	entry, err := st.AddEntry(model.EntryConfig{
		Name:      "Gemini Singleflight",
		Provider:  "gemini_api",
		Kind:      model.KindBalance,
		Enabled:   true,
		Secret:    "AIzaTestSingleflight",
		ProbeMode: "models",
	})
	if err != nil {
		t.Fatalf("add entry: %v", err)
	}

	// 1. Concurrent RefreshOne calls must collapse into a single upstream request.
	var wg sync.WaitGroup
	snaps := make([]model.EntrySnapshot, 4)
	for i := range snaps {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s, ok := pl.RefreshOne(context.Background(), entry.ID)
			if !ok {
				t.Errorf("RefreshOne returned ok=false")
			}
			snaps[idx] = s
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected singleflight to coalesce 4 concurrent refreshes into 1 upstream call, got %d", got)
	}
	for i, s := range snaps {
		if s.Status != model.StatusOK || len(s.Details) == 0 {
			t.Fatalf("snap[%d] expected ok with details, got %+v", i, s)
		}
	}

	// 2. Subsequent upstream 503 preserves previous Details as StatusStale.
	failUpstream.Store(true)
	staleSnap, ok := pl.RefreshOne(context.Background(), entry.ID)
	if !ok || staleSnap.Status != model.StatusStale || len(staleSnap.Details) == 0 || staleSnap.Error == "" {
		t.Fatalf("expected stale snapshot preserving details on outage, got %+v", staleSnap)
	}

	// 3. Disabled entry does not call upstream and reports StatusDisabled.
	beforeCalls := atomic.LoadInt32(&calls)
	entry.Enabled = false
	if _, err := st.UpdateEntry(entry.ID, entry, true); err != nil {
		t.Fatalf("disable entry: %v", err)
	}
	disSnap, ok := pl.RefreshOne(context.Background(), entry.ID)
	if !ok || disSnap.Status != model.StatusDisabled {
		t.Fatalf("expected disabled snapshot, got %+v", disSnap)
	}
	if atomic.LoadInt32(&calls) != beforeCalls {
		t.Fatalf("disabled entry must not trigger upstream request")
	}
}
