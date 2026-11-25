package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/api"
	"github.com/letrahoo/sub-bar/server/internal/model"
	"github.com/letrahoo/sub-bar/server/internal/oauth"
	"github.com/letrahoo/sub-bar/server/internal/poller"
	"github.com/letrahoo/sub-bar/server/internal/providers"
	"github.com/letrahoo/sub-bar/server/internal/store"
)

func newTestServerWithInstance(t *testing.T) (*api.Server, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	reg := providers.NewRegistry()
	pl := poller.New(st, reg)
	srv := api.New(st, reg, pl)
	t.Cleanup(func() { srv.Shutdown() })
	return srv, srv.Handler()
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	_, h := newTestServerWithInstance(t)
	return h
}

func doReq(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:8328"+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndStaticUI(t *testing.T) {
	h := newTestServer(t)

	rec := doReq(t, h, http.MethodGet, "/api/health", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("unexpected health body: %s", rec.Body.String())
	}

	ui := doReq(t, h, http.MethodGet, "/", nil, nil)
	if ui.Code != http.StatusOK || !strings.Contains(ui.Body.String(), "订阅与余额") {
		t.Fatalf("expected embedded high-contrast web UI, got %d", ui.Code)
	}
}

func TestSecurityCSRFGuard(t *testing.T) {
	h := newTestServer(t)

	// Cross-site fetch metadata rejected
	rec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, map[string]string{
		"Sec-Fetch-Site": "cross-site",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-site, got %d", rec.Code)
	}

	// Foreign Origin POST rejected
	rec2 := doReq(t, h, http.MethodPost, "/api/entries", map[string]any{
		"name": "Evil", "provider": "manual",
	}, map[string]string{
		"Origin": "https://evil.example.com",
	})
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign origin, got %d", rec2.Code)
	}
}

func TestIndependentManualSubscriptionAndBalanceLifecycle(t *testing.T) {
	h := newTestServer(t)

	// 1. Add independent manual subscription (e.g., Cursor Pro)
	used := 180.0
	total := 500.0
	cost := 20.0
	createSub := doReq(t, h, http.MethodPost, "/api/entries", map[string]any{
		"name":         "Cursor Pro",
		"provider":     "manual",
		"kind":         "subscription",
		"enabled":      true,
		"pinned":       true,
		"planLabel":    "Pro $20/m",
		"manualCycle":  "monthly",
		"billingDay":   15,
		"quotaUsed":    used,
		"quotaTotal":   total,
		"quotaUnit":    "次",
		"monthlyCost":  cost,
		"costCurrency": "USD",
	}, nil)
	if createSub.Code != http.StatusCreated {
		t.Fatalf("create sub failed (%d): %s", createSub.Code, createSub.Body.String())
	}

	// 2. Add manual balance entry + secret masking check
	bal := 128.5
	createBal := doReq(t, h, http.MethodPost, "/api/entries", map[string]any{
		"name":          "测试储值卡",
		"provider":      "manual",
		"kind":          "balance",
		"enabled":       true,
		"manualBalance": bal,
		"currency":      "CNY",
		"secret":        "sk-super-secret-token-9999",
	}, nil)
	if createBal.Code != http.StatusCreated {
		t.Fatalf("create bal failed (%d): %s", createBal.Code, createBal.Body.String())
	}

	// Ensure GET /api/entries NEVER leaks raw secret
	entriesRec := doReq(t, h, http.MethodGet, "/api/entries", nil, nil)
	if strings.Contains(entriesRec.Body.String(), "sk-super-secret-token-9999") {
		t.Fatalf("secret leaked in /api/entries: %s", entriesRec.Body.String())
	}
	if !strings.Contains(entriesRec.Body.String(), `"hasSecret":true`) {
		t.Fatalf("expected hasSecret:true in /api/entries")
	}

	// Check aggregated snapshot
	snapRec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snap model.SnapshotResponse
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.Summary.TotalEntries != 2 || snap.Summary.SubscriptionCount != 1 || snap.Summary.BalanceCount != 1 {
		t.Fatalf("unexpected summary counts: %+v", snap.Summary)
	}
	if len(snap.Summary.AvailableBalances) != 1 || snap.Summary.AvailableBalances[0].Amount != 128.5 {
		t.Fatalf("unexpected available balances: %+v", snap.Summary.AvailableBalances)
	}
	if len(snap.Summary.MonthlyFixedCosts) != 1 || snap.Summary.MonthlyFixedCosts[0].Amount != 20.0 {
		t.Fatalf("unexpected monthly fixed costs: %+v", snap.Summary.MonthlyFixedCosts)
	}

	// Quick-update quotaUsed on Cursor Pro
	subID := snap.Entries[0].ID
	quickRec := doReq(t, h, http.MethodPost, "/api/entries/"+subID+"/quick-update", map[string]any{
		"quotaUsed": 450.0,
	}, nil)
	if quickRec.Code != http.StatusOK {
		t.Fatalf("quick update failed: %d %s", quickRec.Code, quickRec.Body.String())
	}
	if !strings.Contains(quickRec.Body.String(), `"status":"critical"`) {
		t.Fatalf("expected 450/500 (90%%) to trigger critical status: %s", quickRec.Body.String())
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthInteractiveCallbackAndPollingLifecycle(t *testing.T) {
	for _, id := range []string{"ANTIGRAVITY_OAUTH", "KIMI_OAUTH"} {
		t.Setenv("SUBBAR_OAUTH_"+id+"_CLIENT_ID", "test-client-id")
		t.Setenv("SUBBAR_OAUTH_"+id+"_CLIENT_SECRET", "test-client-secret")
	}
	restore := providers.SetHTTPTransportForTest(rtFunc(func(r *http.Request) (*http.Response, error) {
		mk := func(code int, s string) *http.Response {
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(s)),
			}
		}
		switch {
		case strings.Contains(r.URL.Host, "oauth2.googleapis.com") && r.URL.Path == "/token":
			return mk(200, `{"access_token":"ya29.secret-live","refresh_token":"1//super-refresh-secret","expires_in":3600}`), nil
		case strings.Contains(r.URL.Path, "userinfo"):
			return mk(200, `{"email":"alice@example.com"}`), nil
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			return mk(200, `{"cloudaicompanionProject":"proj-demo","paidTier":{"name":"Google AI Pro"}}`), nil
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			return mk(200, `{"models":{"claude-sonnet-4-5":{"quotaInfo":{"remainingFraction":0.75,"resetTime":"2026-11-16T02:00:00Z"}}}}`), nil
		default:
			return mk(404, `{}`), nil
		}
	}))
	defer restore()

	srv, h := newTestServerWithInstance(t)

	// 1. Start OAuth session for Antigravity (agy) without even typing a name
	startRec := doReq(t, h, http.MethodPost, "/api/oauth/start", map[string]any{
		"provider": "antigravity_oauth",
		"pinned":   true,
	}, nil)
	if startRec.Code != http.StatusOK {
		t.Fatalf("oauth start failed (%d): %s", startRec.Code, startRec.Body.String())
	}
	var startResp struct {
		State        string `json:"state"`
		AuthURL      string `json:"authUrl"`
		CallbackPort int    `json:"callbackPort"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if startResp.State == "" || !strings.Contains(startResp.AuthURL, "accounts.google.com") || startResp.CallbackPort != 51121 {
		t.Fatalf("unexpected oauth start response: %+v", startResp)
	}

	// 2. Poll status -> pending
	poll1 := doReq(t, h, http.MethodGet, "/api/oauth/status?state="+startResp.State, nil, nil)
	if poll1.Code != http.StatusOK || !strings.Contains(poll1.Body.String(), `"status":"pending"`) {
		t.Fatalf("expected pending status, got %d %s", poll1.Code, poll1.Body.String())
	}

	// 3. Simulate browser redirect hitting the loopback CallbackHandler
	cbReq := httptest.NewRequest(http.MethodGet, "http://localhost:51121/oauth-callback?code=4/0AdemoCode&state="+startResp.State, nil)
	cbRec := httptest.NewRecorder()
	srv.OAuthManager().CallbackHandler().ServeHTTP(cbRec, cbReq)
	if cbRec.Code != http.StatusOK || !strings.Contains(cbRec.Body.String(), "授权成功") {
		t.Fatalf("callback HTML failed (%d): %s", cbRec.Code, cbRec.Body.String())
	}

	// 4. Poll status -> completed with auto-named entry & live quota snapshot, and ZERO leaked secrets!
	poll2 := doReq(t, h, http.MethodGet, "/api/oauth/status?state="+startResp.State, nil, nil)
	if poll2.Code != http.StatusOK || !strings.Contains(poll2.Body.String(), `"status":"completed"`) {
		t.Fatalf("expected completed status, got %d %s", poll2.Code, poll2.Body.String())
	}
	if !strings.Contains(poll2.Body.String(), `"Antigravity (alice)"`) {
		t.Fatalf("expected auto-filled name Antigravity (alice), got %s", poll2.Body.String())
	}
	if strings.Contains(poll2.Body.String(), "ya29.secret-live") || strings.Contains(poll2.Body.String(), "1//super-refresh-secret") {
		t.Fatalf("oauth tokens leaked in status endpoint: %s", poll2.Body.String())
	}

	// 5. Verify /api/entries also hides refreshToken & secret while marking hasRefreshToken:true
	entRec := doReq(t, h, http.MethodGet, "/api/entries", nil, nil)
	if strings.Contains(entRec.Body.String(), "1//super-refresh-secret") || strings.Contains(entRec.Body.String(), "ya29.secret-live") {
		t.Fatalf("tokens leaked in /api/entries: %s", entRec.Body.String())
	}
	if !strings.Contains(entRec.Body.String(), `"hasRefreshToken":true`) {
		t.Fatalf("expected hasRefreshToken:true in /api/entries: %s", entRec.Body.String())
	}
}

func TestNoGeminiCLIAndInitialErrorVsPostBalanceAbnormal(t *testing.T) {
	failDeepseek := false
	restore := providers.SetHTTPTransportForTest(rtFunc(func(r *http.Request) (*http.Response, error) {
		mk := func(code int, s string) *http.Response {
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(s)),
			}
		}
		if strings.Contains(r.URL.Host, "anthropic.com") {
			return mk(429, `{"error":{"message":"rate limited"}}`), nil
		}
		if strings.Contains(r.URL.Host, "deepseek.com") {
			if failDeepseek {
				return mk(503, `{"error":"upstream maintenance"}`), nil
			}
			return mk(200, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"25.00"}]}`), nil
		}
		return mk(404, `{}`), nil
	}))
	defer restore()

	h := newTestServer(t)

	// 1. Verify Gemini CLI is removed from providers and local discovery while Antigravity is present
	provRec := doReq(t, h, http.MethodGet, "/api/providers", nil, nil)
	if strings.Contains(provRec.Body.String(), "gemini_oauth") {
		t.Fatalf("gemini_oauth should not be listed in /api/providers")
	}
	if !strings.Contains(provRec.Body.String(), "antigravity_oauth") {
		t.Fatalf("antigravity_oauth must be listed in /api/providers")
	}
	discRec := doReq(t, h, http.MethodGet, "/api/discover", nil, nil)
	if strings.Contains(discRec.Body.String(), "gemini_local") {
		t.Fatalf("gemini_local should not be returned by /api/discover")
	}

	// 2. Add an entry that fails on its very first fetch (initial error, zero balance/windows)
	c1 := doReq(t, h, http.MethodPost, "/api/entries", map[string]any{
		"name":     "Claude Broken Init",
		"provider": "claude_oauth",
		"kind":     "subscription",
		"enabled":  true,
		"pinned":   true,
		"authMode": "token",
		"secret":   "sk-ant-oat01-init-429",
	}, nil)
	if c1.Code != http.StatusCreated {
		t.Fatalf("create initial-error entry failed: %d %s", c1.Code, c1.Body.String())
	}

	snap1Rec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snap1 model.SnapshotResponse
	_ = json.Unmarshal(snap1Rec.Body.Bytes(), &snap1)
	if snap1.Summary.CriticalCount != 0 || snap1.Summary.WarningCount != 0 {
		t.Fatalf("initial-error entry without prior balance/quota must not trigger summary alerts, got %+v", snap1.Summary)
	}

	// 3. Add a DeepSeek entry that first acquires balance ($25.00), then fails on refresh -> preserves balance & triggers alert
	c2 := doReq(t, h, http.MethodPost, "/api/entries", map[string]any{
		"name":     "DeepSeek API",
		"provider": "deepseek",
		"kind":     "balance",
		"enabled":  true,
		"pinned":   true,
		"currency": "USD",
		"secret":   "sk-ds-demo",
	}, nil)
	if c2.Code != http.StatusCreated {
		t.Fatalf("create deepseek failed: %d %s", c2.Code, c2.Body.String())
	}
	var c2Body struct {
		Entry model.EntryConfigPublic `json:"entry"`
	}
	_ = json.Unmarshal(c2.Body.Bytes(), &c2Body)

	failDeepseek = true
	refRec := doReq(t, h, http.MethodPost, "/api/entries/"+c2Body.Entry.ID+"/refresh", nil, nil)
	if refRec.Code != http.StatusOK {
		t.Fatalf("refresh deepseek failed: %d", refRec.Code)
	}

	snap2Rec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snap2 model.SnapshotResponse
	_ = json.Unmarshal(snap2Rec.Body.Bytes(), &snap2)
	if snap2.Summary.WarningCount != 1 {
		t.Fatalf("post-balance abnormal entry must increment WarningCount (stale), got %+v", snap2.Summary)
	}
	if len(snap2.Summary.AvailableBalances) != 1 || snap2.Summary.AvailableBalances[0].Amount != 25.0 {
		t.Fatalf("post-balance abnormal entry must preserve its $25.00 balance, got %+v", snap2.Summary.AvailableBalances)
	}
}

func TestKimiDeviceOAuthWeChatFlowLifecycle(t *testing.T) {
	for _, id := range []string{"ANTIGRAVITY_OAUTH", "KIMI_OAUTH"} {
		t.Setenv("SUBBAR_OAUTH_"+id+"_CLIENT_ID", "test-client-id")
		t.Setenv("SUBBAR_OAUTH_"+id+"_CLIENT_SECRET", "test-client-secret")
	}
	restorePoll := oauth.SetDevicePollIntervalForTest(25 * time.Millisecond)
	defer restorePoll()

	var tokenPollCalls int32
	restoreHTTP := providers.SetHTTPTransportForTest(rtFunc(func(r *http.Request) (*http.Response, error) {
		mk := func(code int, s string) *http.Response {
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(s)),
			}
		}
		switch {
		case r.URL.Host == "auth.kimi.com" && r.URL.Path == "/api/oauth/device_authorization":
			return mk(200, `{
				"device_code": "dev-code-wechat-1",
				"user_code": "WXQR-8899",
				"verification_uri": "https://www.kimi.com/code/authorize_device",
				"verification_uri_complete": "https://www.kimi.com/code/authorize_device?user_code=WXQR-8899",
				"expires_in": 600,
				"interval": 5
			}`), nil
		case r.URL.Host == "auth.kimi.com" && r.URL.Path == "/api/oauth/token":
			n := atomic.AddInt32(&tokenPollCalls, 1)
			if n == 1 {
				return mk(400, `{"error":"authorization_pending","error_description":"Authorization is pending"}`), nil
			}
			return mk(200, `{
				"access_token": "kimi-at-secret-999",
				"refresh_token": "kimi-rt-secret-999",
				"expires_in": 3600
			}`), nil
		case r.URL.Host == "api.kimi.com" && r.URL.Path == "/coding/v1/usages":
			return mk(200, `{
				"user": {"nickname": "Alice", "membership": {"level": "LEVEL_INTERMEDIATE"}},
				"usage": {"limit": "100", "used": "12", "remaining": "88", "resetTime": "2026-11-22T00:00:00Z"},
				"limits": [{"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}, "detail": {"limit": "100", "used": "5", "remaining": "95", "resetTime": "2026-11-16T04:00:00Z"}}]
			}`), nil
		default:
			return mk(404, `{}`), nil
		}
	}))
	defer restoreHTTP()

	h := newTestServer(t)

	// 1. Start Kimi device-code OAuth session (supports WeChat QR scan on www.kimi.com)
	startRec := doReq(t, h, http.MethodPost, "/api/oauth/start", map[string]any{
		"provider": "kimi_oauth",
		"pinned":   true,
	}, nil)
	if startRec.Code != http.StatusOK {
		t.Fatalf("kimi oauth start failed (%d): %s", startRec.Code, startRec.Body.String())
	}
	var startResp struct {
		State        string `json:"state"`
		AuthURL      string `json:"authUrl"`
		CallbackMode string `json:"callbackMode"`
		UserCode     string `json:"userCode"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if startResp.CallbackMode != "device" || startResp.UserCode != "WXQR-8899" || !strings.Contains(startResp.AuthURL, "www.kimi.com/code/authorize_device?user_code=WXQR-8899") {
		t.Fatalf("unexpected kimi device start response: %+v", startResp)
	}

	// 2. Poll /api/oauth/status until background device poller completes exchange + initial quota fetch
	var finalBody string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pollRec := doReq(t, h, http.MethodGet, "/api/oauth/status?state="+startResp.State, nil, nil)
		if strings.Contains(pollRec.Body.String(), `"status":"completed"`) {
			finalBody = pollRec.Body.String()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if finalBody == "" {
		t.Fatalf("timed out waiting for kimi device oauth completion")
	}
	if !strings.Contains(finalBody, `"Kimi 订阅 (Alice)"`) || !strings.Contains(finalBody, `"Moderato"`) {
		t.Fatalf("expected auto-named Kimi 订阅 (Alice) with Moderato plan, got %s", finalBody)
	}
	if strings.Contains(finalBody, "kimi-at-secret-999") || strings.Contains(finalBody, "kimi-rt-secret-999") {
		t.Fatalf("kimi tokens leaked in /api/oauth/status: %s", finalBody)
	}
}

func TestGeminiAPIEntryLifecycleAndSummaryMetrics(t *testing.T) {
	phase := "ok"
	restore := providers.SetHTTPTransportForTest(rtFunc(func(r *http.Request) (*http.Response, error) {
		mk := func(code int, s string) *http.Response {
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(s)),
			}
		}
		if r.URL.Host != "generativelanguage.googleapis.com" {
			return mk(404, `{}`), nil
		}
		if phase == "outage" {
			return mk(503, `{"error":{"code":503,"message":"service unavailable"}}`), nil
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			return mk(200, `{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]}]}`), nil
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":generateContent") {
			switch phase {
			case "ok":
				return mk(200, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`), nil
			case "429":
				return mk(429, `{"error":{"code":429,"message":"Rate limit exceeded","status":"RESOURCE_EXHAUSTED"}}`), nil
			case "402":
				return mk(402, `{"error":{"code":402,"message":"Prepaid balance depleted"}}`), nil
			}
		}
		return mk(404, `{}`), nil
	}))
	defer restore()

	h := newTestServer(t)

	// 1. Create gemini_api entry
	createRec := doReq(t, h, http.MethodPost, "/api/entries", map[string]any{
		"name":      "Gemini AI Studio",
		"provider":  "gemini_api",
		"enabled":   true,
		"pinned":    true,
		"probeMode": "generate",
		"secret":    "AIza-secret-never-leak-9999",
	}, nil)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create gemini_api failed (%d): %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Entry    model.EntryConfigPublic `json:"entry"`
		Snapshot model.EntrySnapshot     `json:"snapshot"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Snapshot.Status != model.StatusOK || len(created.Snapshot.Details) == 0 {
		t.Fatalf("expected healthy snapshot with details, got %+v", created.Snapshot)
	}
	if created.Snapshot.DashboardURL != "https://aistudio.google.com/billing" {
		t.Fatalf("expected AI Studio billing dashboard URL, got %q", created.Snapshot.DashboardURL)
	}

	// 2. Verify secret is never leaked and probeMode is preserved in /api/entries
	entRec := doReq(t, h, http.MethodGet, "/api/entries", nil, nil)
	if strings.Contains(entRec.Body.String(), "AIza-secret-never-leak-9999") {
		t.Fatalf("gemini api key leaked in /api/entries")
	}
	if !strings.Contains(entRec.Body.String(), `"probeMode":"generate"`) {
		t.Fatalf("expected probeMode persisted in /api/entries: %s", entRec.Body.String())
	}

	// 3. Verify snapshot summary does NOT pollute AvailableBalances with a fake $0.00
	snapRec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snap model.SnapshotResponse
	_ = json.Unmarshal(snapRec.Body.Bytes(), &snap)
	if snap.Summary.BalanceCount != 1 || len(snap.Summary.AvailableBalances) != 0 {
		t.Fatalf("expected BalanceCount=1 with empty AvailableBalances, got %+v", snap.Summary)
	}

	// 4. Transition to 429 -> WarningCount=1
	phase = "429"
	ref429 := doReq(t, h, http.MethodPost, "/api/entries/"+created.Entry.ID+"/refresh", nil, nil)
	if ref429.Code != http.StatusOK {
		t.Fatalf("refresh 429 failed: %d", ref429.Code)
	}
	snap429Rec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snap429 model.SnapshotResponse
	_ = json.Unmarshal(snap429Rec.Body.Bytes(), &snap429)
	if snap429.Summary.WarningCount != 1 || snap429.Entries[0].Status != model.StatusWarning {
		t.Fatalf("expected WarningCount=1 on 429, got summary=%+v status=%s", snap429.Summary, snap429.Entries[0].Status)
	}

	// 5. Transition to 402 -> CriticalCount=1
	phase = "402"
	_ = doReq(t, h, http.MethodPost, "/api/entries/"+created.Entry.ID+"/refresh", nil, nil)
	snap402Rec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snap402 model.SnapshotResponse
	_ = json.Unmarshal(snap402Rec.Body.Bytes(), &snap402)
	if snap402.Summary.CriticalCount != 1 || snap402.Entries[0].Status != model.StatusCritical {
		t.Fatalf("expected CriticalCount=1 on 402, got summary=%+v status=%s", snap402.Summary, snap402.Entries[0].Status)
	}

	// 6. Transient upstream 503 preserves previous Details as StatusStale
	phase = "outage"
	_ = doReq(t, h, http.MethodPost, "/api/entries/"+created.Entry.ID+"/refresh", nil, nil)
	snapStaleRec := doReq(t, h, http.MethodGet, "/api/snapshot", nil, nil)
	var snapStale model.SnapshotResponse
	_ = json.Unmarshal(snapStaleRec.Body.Bytes(), &snapStale)
	if snapStale.Entries[0].Status != model.StatusStale || len(snapStale.Entries[0].Details) == 0 {
		t.Fatalf("expected transient 503 to preserve cached details as stale, got %+v", snapStale.Entries[0])
	}
}

func TestOAuthReAddSameAccountDeduplicatesInsteadOfCreatingDuplicate(t *testing.T) {
	t.Setenv("SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID", "test-client-id")
	t.Setenv("SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_SECRET", "test-client-secret")

	tokenCalls := 0
	restore := providers.SetHTTPTransportForTest(rtFunc(func(r *http.Request) (*http.Response, error) {
		mk := func(code int, s string) *http.Response {
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(s)),
			}
		}
		switch {
		case strings.Contains(r.URL.Host, "oauth2.googleapis.com") && r.URL.Path == "/token":
			tokenCalls++
			if tokenCalls == 1 {
				return mk(200, `{"access_token":"ya29.first","refresh_token":"1//rt-first","expires_in":3600}`), nil
			}
			return mk(200, `{"access_token":"ya29.second","refresh_token":"1//rt-second","expires_in":3600}`), nil
		case strings.Contains(r.URL.Path, "userinfo"):
			return mk(200, `{"email":"alice@example.com"}`), nil
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			return mk(200, `{"cloudaicompanionProject":"proj-demo","paidTier":{"name":"Google AI Ultra"}}`), nil
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			return mk(200, `{"models":{"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.8,"resetTime":"2026-11-26T02:00:00Z"}}}}`), nil
		default:
			return mk(404, `{}`), nil
		}
	}))
	defer restore()

	srv, h := newTestServerWithInstance(t)

	completeOAuth := func(payload map[string]any) {
		t.Helper()
		startRec := doReq(t, h, http.MethodPost, "/api/oauth/start", payload, nil)
		if startRec.Code != http.StatusOK {
			t.Fatalf("oauth start failed: %d %s", startRec.Code, startRec.Body.String())
		}
		var startResp struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(startRec.Body.Bytes(), &startResp)

		cbReq := httptest.NewRequest(http.MethodGet, "http://localhost:51121/oauth-callback?code=4/code&state="+startResp.State, nil)
		cbRec := httptest.NewRecorder()
		srv.OAuthManager().CallbackHandler().ServeHTTP(cbRec, cbReq)
		if cbRec.Code != http.StatusOK {
			t.Fatalf("oauth callback failed: %d", cbRec.Code)
		}
	}

	// 1. First OAuth login with custom name and pinned=true
	completeOAuth(map[string]any{
		"provider": "antigravity_oauth",
		"name":     "主力 Antigravity",
		"pinned":   true,
	})

	// 2. Second OAuth login for the EXACT same account without entryId or name (via "Add")
	completeOAuth(map[string]any{
		"provider": "antigravity_oauth",
		"pinned":   false,
	})

	entRec := doReq(t, h, http.MethodGet, "/api/entries", nil, nil)
	var entEnv struct {
		Entries []model.EntryConfigPublic `json:"entries"`
	}
	if err := json.Unmarshal(entRec.Body.Bytes(), &entEnv); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(entEnv.Entries) != 1 {
		t.Fatalf("expected duplicate OAuth login to merge into 1 entry, got %d: %+v", len(entEnv.Entries), entEnv.Entries)
	}
	if entEnv.Entries[0].Name != "主力 Antigravity" || !entEnv.Entries[0].Pinned {
		t.Fatalf("expected existing custom name & pinned state preserved, got %+v", entEnv.Entries[0])
	}
}
