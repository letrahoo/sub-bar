package providers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/letrahoo/sub-bar/server/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withMockHTTP(t *testing.T, fn roundTripFunc) {
	t.Helper()
	orig := sharedHTTPClient
	sharedHTTPClient = &http.Client{Transport: fn}
	t.Cleanup(func() { sharedHTTPClient = orig })
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestCodexOAuthFetch(t *testing.T) {
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-jwt" {
			t.Fatalf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-Id") != "ws-123" {
			t.Fatalf("expected account id header, got %s", r.Header.Get("ChatGPT-Account-Id"))
		}
		return jsonResp(200, `{
			"plan_type": "pro",
			"rate_limit": {
				"allowed": true,
				"limit_reached": false,
				"primary_window": {"used_percent": 32, "limit_window_seconds": 604800, "reset_after_seconds": 300000},
				"secondary_window": {"used_percent": 15, "limit_window_seconds": 18000, "reset_after_seconds": 7200}
			}
		}`), nil
	})

	reg := NewRegistry()
	prov, _ := reg.Get("codex_oauth")
	res, err := prov.Fetch(context.Background(), model.EntryConfig{
		AuthMode:  "token",
		Secret:    "test-jwt",
		AccountID: "ws-123",
	})
	if err != nil {
		t.Fatalf("codex fetch err: %v", err)
	}
	if res.PlanLabel != "PRO" || len(res.Windows) != 2 {
		t.Fatalf("unexpected codex result: %+v", res)
	}
	// Shorter 5h window should be ordered first
	if res.Windows[0].WindowSeconds != 18000 || res.Windows[0].RemainingPercent != 85 {
		t.Fatalf("expected 5h window first with 85%% remaining, got %+v", res.Windows[0])
	}
}

func TestClaudeOAuthFetch(t *testing.T) {
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Fatalf("missing anthropic-beta header")
		}
		return jsonResp(200, `{
			"five_hour": {"utilization": 78.0, "resets_at": "2026-11-15T23:00:00Z"},
			"seven_day": {"utilization": 22.0, "resets_at": "2026-11-20T00:00:00Z"},
			"extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 1250, "utilization": 25.0, "currency": "USD"}
		}`), nil
	})

	reg := NewRegistry()
	prov, _ := reg.Get("claude_oauth")
	res, err := prov.Fetch(context.Background(), model.EntryConfig{
		AuthMode: "token",
		Secret:   "sk-ant-oat01-demo",
	})
	if err != nil {
		t.Fatalf("claude fetch err: %v", err)
	}
	if res.Status != model.StatusWarning {
		t.Fatalf("expected warning status for 78%% 5h utilization, got %s", res.Status)
	}
	if res.Balance == nil || res.Balance.Available != 37.5 {
		t.Fatalf("expected extra_usage available $37.50, got %+v", res.Balance)
	}
}

func TestDeepSeekAndOpenRouterFallback(t *testing.T) {
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "deepseek.com"):
			return jsonResp(200, `{
				"is_available": true,
				"balance_infos": [
					{"currency": "CNY", "total_balance": "86.40", "granted_balance": "10.00", "topped_up_balance": "76.40"}
				]
			}`), nil
		case strings.HasSuffix(r.URL.Path, "/credits"):
			return jsonResp(403, `{"error":"management key required"}`), nil
		case strings.HasSuffix(r.URL.Path, "/key"):
			return jsonResp(200, `{"data":{"label":"dev","usage":4.2,"limit":20.0,"limit_remaining":15.8}}`), nil
		default:
			return jsonResp(404, `{}`), nil
		}
	})

	reg := NewRegistry()
	ds, _ := reg.Get("deepseek")
	dsRes, err := ds.Fetch(context.Background(), model.EntryConfig{Secret: "sk-ds", Currency: "CNY"})
	if err != nil || dsRes.Balance == nil || dsRes.Balance.Available != 86.4 {
		t.Fatalf("deepseek unexpected: %v %+v", err, dsRes)
	}

	orProv, _ := reg.Get("openrouter")
	orRes, err := orProv.Fetch(context.Background(), model.EntryConfig{Secret: "sk-or-v1-regular"})
	if err != nil || orRes.Balance == nil || orRes.Balance.Available != 15.8 {
		t.Fatalf("openrouter /key fallback failed: %v %+v", err, orRes)
	}
}

func TestCustomHTTPDotPathBothModes(t *testing.T) {
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{
			"data": {
				"quota": 5000000,
				"used": 1250000,
				"reset_at": 1800000000
			}
		}`), nil
	})

	reg := NewRegistry()
	ch, _ := reg.Get("custom_http")

	// 1. Balance mode with NewAPI quota scale (0.000002 -> $10.00)
	balRes, err := ch.Fetch(context.Background(), model.EntryConfig{
		Kind:        model.KindBalance,
		BaseURL:     "https://relay.example.com/api/user/self",
		BalancePath: "data.quota",
		Scale:       0.000002,
		Currency:    "USD",
	})
	if err != nil || balRes.Balance == nil || balRes.Balance.Available != 10.0 {
		t.Fatalf("custom_http balance failed: %v %+v", err, balRes)
	}

	// 2. Subscription mode computing used/limit percentage (1.25M / 5M = 25%)
	subRes, err := ch.Fetch(context.Background(), model.EntryConfig{
		Kind:        model.KindSubscription,
		BaseURL:     "https://relay.example.com/api/user/self",
		UsedPath:    "data.used",
		LimitPath:   "data.quota",
		ResetAtPath: "data.reset_at",
	})
	if err != nil || len(subRes.Windows) != 1 || subRes.Windows[0].UsedPercent != 25.0 {
		t.Fatalf("custom_http subscription failed: %v %+v", err, subRes)
	}
}

func TestAntigravityAutoRefreshAndModelGrouping(t *testing.T) {
	refreshed := false
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "oauth2.googleapis.com") && r.URL.Path == "/token":
			refreshed = true
			return jsonResp(200, `{
				"access_token": "ya29.fresh-agy-token",
				"expires_in": 3600
			}`), nil
		case strings.Contains(r.URL.Path, "userinfo"):
			return jsonResp(200, `{"email": "alice@example.com"}`), nil
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			if r.Header.Get("Authorization") != "Bearer ya29.fresh-agy-token" {
				return jsonResp(401, `{"error":"expired"}`), nil
			}
			return jsonResp(200, `{
				"cloudaicompanionProject": {"id": "agy-proj-42"},
				"paidTier": {"id": "g1-pro-tier", "name": "Google AI Pro"}
			}`), nil
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			return jsonResp(200, `{
				"models": {
					"claude-sonnet-4-5": {
						"displayName": "Claude Sonnet 4.5",
						"quotaInfo": {"remainingFraction": 0.65, "resetTime": "2026-11-16T01:00:00Z"}
					},
					"gpt-oss-120b": {
						"displayName": "GPT OSS",
						"quotaInfo": {"remainingFraction": 0.65, "resetTime": "2026-11-16T01:00:00Z"}
					},
					"gemini-3-pro-high": {
						"displayName": "Gemini 3 Pro",
						"quotaInfo": {"remainingFraction": 0.90, "resetTime": "2026-11-16T01:30:00Z"}
					},
					"gemini-3-flash": {
						"displayName": "Gemini 3 Flash",
						"quotaInfo": {"resetTime": "2026-11-16T00:45:00Z"}
					},
					"tab_autocomplete_v2": {
						"displayName": "Tab",
						"quotaInfo": {"remainingFraction": 1.0}
					}
				}
			}`), nil
		default:
			return jsonResp(404, `{}`), nil
		}
	})

	reg := NewRegistry()
	agy, ok := reg.Get("antigravity_oauth")
	if !ok {
		t.Fatalf("antigravity_oauth provider not registered")
	}

	res, err := agy.Fetch(context.Background(), model.EntryConfig{
		AuthMode:     "oauth",
		Secret:       "ya29.stale-token",
		RefreshToken: "1//refresh-agy",
	})
	if err != nil {
		t.Fatalf("antigravity fetch failed: %v", err)
	}
	if !refreshed || res.TokenUpdate == nil || res.TokenUpdate.AccessToken != "ya29.fresh-agy-token" || res.TokenUpdate.ProjectID != "agy-proj-42" {
		t.Fatalf("expected reactive 401 token refresh + project persistence, got %+v", res.TokenUpdate)
	}
	if res.PlanLabel != "AI Pro" {
		t.Fatalf("expected AI Pro tier label, got %q", res.PlanLabel)
	}
	if len(res.Windows) != 3 {
		t.Fatalf("expected 3 deduplicated windows (Gemini Pro, Gemini Flash, Claude/GPT), got %+v", res.Windows)
	}
	if res.Windows[0].Label != "Gemini Pro" || res.Windows[0].UsedPercent != 10.0 {
		t.Fatalf("expected Gemini Pro first with 10%% used, got %+v", res.Windows[0])
	}
	if res.Windows[1].Label != "Gemini Flash" || res.Windows[1].UsedPercent != 100.0 || res.Status != model.StatusCritical {
		t.Fatalf("expected proto3 omitted remainingFraction to report 100%% exhausted critical, got %+v (status %s)", res.Windows[1], res.Status)
	}
	if res.Windows[2].Label != "Claude / GPT" || res.Windows[2].UsedPercent != 35.0 {
		t.Fatalf("expected merged Claude / GPT 35%% used, got %+v", res.Windows[2])
	}
}

func TestAntigravityDailyHostAndQuotaSummaryFusion(t *testing.T) {
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		// Simulate prod cloudcode-pa returning misleading 100% while daily-cloudcode-pa has actual `agy` consumption.
		if r.URL.Host == "cloudcode-pa.googleapis.com" {
			return jsonResp(200, `{
				"models": {
					"gemini-2.5-pro": {"quotaInfo": {"remainingFraction": 1.0, "resetTime": "2026-11-15T19:00:00Z"}},
					"gemini-3-flash": {"quotaInfo": {"remainingFraction": 1.0, "resetTime": "2026-11-15T19:00:00Z"}}
				}
			}`), nil
		}
		if r.URL.Host != "daily-cloudcode-pa.googleapis.com" {
			return jsonResp(404, `{}`), nil
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			return jsonResp(200, `{
				"cloudaicompanionProject": "aicode-consumers",
				"paidTier": {
					"id": "g1-ultra-lite-tier",
					"name": "Google AI Ultra",
					"availableCredits": [{"creditType": "GOOGLE_ONE_AI", "creditAmount": "420"}]
				}
			}`), nil
		case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
			return jsonResp(200, `{
				"groups": [{
					"displayName": "Claude and GPT models",
					"buckets": [
						{"bucketId": "3p-weekly", "displayName": "Weekly Limit Remaining", "window": "weekly", "resetTime": "2026-11-22T07:36:18Z", "remainingFraction": 0.92},
						{"bucketId": "3p-5h", "displayName": "Five Hour Limit Remaining", "window": "5h", "resetTime": "2026-11-15T19:10:25Z", "remainingFraction": 1.0}
					]
				}]
			}`), nil
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			return jsonResp(200, `{
				"models": {
					"gemini-2.5-pro": {"quotaInfo": {"remainingFraction": 0.60, "resetTime": "2026-11-15T17:17:05Z"}},
					"gemini-3-flash": {"quotaInfo": {"remainingFraction": 0.60, "resetTime": "2026-11-15T17:17:05Z"}},
					"gemini-3.5-flash-lite": {"quotaInfo": {"remainingFraction": 0.60, "resetTime": "2026-11-15T17:17:05Z"}},
					"claude-opus-5-5-high": {"quotaInfo": {"remainingFraction": 1.0, "resetTime": "2026-11-15T19:10:25Z"}}
				}
			}`), nil
		default:
			return jsonResp(404, `{}`), nil
		}
	})

	reg := NewRegistry()
	agy, _ := reg.Get("antigravity_oauth")
	res, err := agy.Fetch(context.Background(), model.EntryConfig{
		AuthMode: "oauth",
		Secret:   "ya29.live",
	})
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	if res.PlanLabel != "AI Ultra · 420 Credits" {
		t.Fatalf("expected AI Ultra · 420 Credits, got %q", res.PlanLabel)
	}
	if len(res.Windows) != 3 {
		t.Fatalf("expected 3 fused windows (Gemini 5h, Claude/GPT 5h, Claude/GPT weekly), got %+v", res.Windows)
	}
	if res.Windows[0].Label != "Gemini (5h)" || res.Windows[0].RemainingPercent != 60.0 {
		t.Fatalf("expected Gemini (5h) 60%% remaining from daily-cloudcode-pa, got %+v", res.Windows[0])
	}
	if res.Windows[1].Label != "Claude / GPT (5h)" || res.Windows[1].WindowSeconds != 18000 {
		t.Fatalf("expected Claude / GPT (5h) second, got %+v", res.Windows[1])
	}
	if res.Windows[2].Label != "Claude / GPT (周限额)" || res.Windows[2].RemainingPercent != 92.0 || res.Windows[2].WindowSeconds != 604800 {
		t.Fatalf("expected Claude / GPT weekly 92%% remaining, got %+v", res.Windows[2])
	}
}

func TestKimiSubscriptionRefreshAndWebBillingFallback(t *testing.T) {
	refreshed := false
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "auth.kimi.com" && r.URL.Path == "/api/oauth/token":
			if r.Header.Get("X-Msh-Platform") != "kimi_cli" {
				t.Fatalf("missing X-Msh-Platform header on Kimi token refresh")
			}
			refreshed = true
			return jsonResp(200, `{
				"access_token": "kimi-fresh-at",
				"refresh_token": "kimi-fresh-rt",
				"expires_in": 3600
			}`), nil
		case r.URL.Host == "api.kimi.com" && r.URL.Path == "/coding/v1/usages":
			if !strings.HasPrefix(r.Header.Get("User-Agent"), "KimiCLI/") {
				t.Fatalf("expected KimiCLI User-Agent on /coding/v1/usages, got %q", r.Header.Get("User-Agent"))
			}
			auth := r.Header.Get("Authorization")
			if auth == "Bearer kimi-web-cookie-jwt" {
				return jsonResp(403, `{"error":"access_terminated_error"}`), nil
			}
			if auth != "Bearer kimi-fresh-at" {
				return jsonResp(401, `{"error":"token_expired"}`), nil
			}
			return jsonResp(200, `{
				"user": {
					"nickname": "Alice",
					"membership": {"level": "LEVEL_INTERMEDIATE"}
				},
				"usage": {
					"limit": "100",
					"used": "18",
					"remaining": "82",
					"resetTime": "2026-11-22T00:00:00Z"
				},
				"limits": [
					{
						"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"},
						"detail": {"limit": "100", "remaining": "93", "resetTime": "2026-11-16T04:00:00Z"}
					}
				]
			}`), nil
		case r.URL.Host == "www.kimi.com" && strings.Contains(r.URL.Path, "BillingService/GetUsages"):
			if r.Header.Get("Authorization") != "Bearer kimi-web-cookie-jwt" || r.Header.Get("connect-protocol-version") != "1" {
				t.Fatalf("unexpected web billing headers: %+v", r.Header)
			}
			return jsonResp(200, `{
				"usages": [
					{
						"scope": "FEATURE_CODING",
						"detail": {"limit": "200", "used": "40", "remaining": "160", "resetTime": "2026-11-22T00:00:00Z"},
						"limits": [
							{
								"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"},
								"detail": {"limit": "100", "used": "10", "remaining": "90", "resetTime": "2026-11-16T04:00:00Z"}
							}
						]
					}
				]
			}`), nil
		default:
			return jsonResp(404, `{}`), nil
		}
	})

	reg := NewRegistry()
	kimi, ok := reg.Get("kimi_oauth")
	if !ok {
		t.Fatalf("kimi_oauth provider not registered")
	}

	// 1. OAuth flow with reactive 401 refresh + proto3 omitted `used` in 5h detail (limit=100, remaining=93 -> used=7%)
	res, err := kimi.Fetch(context.Background(), model.EntryConfig{
		AuthMode:     "oauth",
		Secret:       "kimi-stale-at",
		RefreshToken: "kimi-rt-001",
		AccountID:    "dev-uuid-123",
	})
	if err != nil {
		t.Fatalf("kimi oauth fetch failed: %v", err)
	}
	if !refreshed || res.TokenUpdate == nil || res.TokenUpdate.AccessToken != "kimi-fresh-at" {
		t.Fatalf("expected reactive Kimi token refresh, got %+v", res.TokenUpdate)
	}
	if res.PlanLabel != "Moderato" || res.AccountEmail != "Alice" {
		t.Fatalf("expected Moderato / Alice, got plan=%q acct=%q", res.PlanLabel, res.AccountEmail)
	}
	if len(res.Windows) != 2 || res.Windows[0].WindowSeconds != 18000 || res.Windows[0].UsedPercent != 7.0 || res.Windows[1].UsedPercent != 18.0 {
		t.Fatalf("unexpected kimi windows: %+v", res.Windows)
	}

	// 2. Manual `Cookie: kimi-auth=...` string falling back cleanly to Connect-RPC BillingService/GetUsages
	webRes, err := kimi.Fetch(context.Background(), model.EntryConfig{
		Secret: "Cookie: foo=bar; kimi-auth=kimi-web-cookie-jwt; Path=/",
	})
	if err != nil {
		t.Fatalf("kimi web cookie fallback failed: %v", err)
	}
	if len(webRes.Windows) != 2 || webRes.Windows[0].UsedPercent != 10.0 || webRes.Windows[1].UsedPercent != 20.0 || webRes.Windows[1].UsedDisplay != "已用 40/200" {
		t.Fatalf("unexpected kimi web billing windows: %+v", webRes.Windows)
	}
}

func TestAntigravityAICodeFetchQuotaStatusWeeklyAndLegacyCLIFallback(t *testing.T) {
	scopeInsufficient := false
	withMockHTTP(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "aicode.googleapis.com" && r.URL.Path == "/v1alpha:fetchQuotaStatus":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET on fetchQuotaStatus, got %s", r.Method)
			}
			if scopeInsufficient {
				return jsonResp(403, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`), nil
			}
			return jsonResp(200, `{
				"quotaSummary": {
					"groups": [
						{
							"displayName": "Gemini Models",
							"buckets": [
								{"bucketId": "gemini-weekly", "displayName": "Weekly Limit Remaining", "refreshWindowLabel": "weekly", "remainingFraction": 0.76, "resetTime": "2026-11-22T07:17:05Z"},
								{"bucketId": "gemini-5h", "displayName": "Five Hour Limit Remaining", "refreshWindowLabel": "5h", "remainingFraction": 0.44, "resetTime": "2026-11-15T17:17:05Z"}
							]
						},
						{
							"displayName": "Claude and GPT models",
							"buckets": [
								{"bucketId": "3p-weekly", "displayName": "Weekly Limit Remaining", "refreshWindowLabel": "weekly", "remainingFraction": 1.0, "resetTime": "2026-11-22T07:36:18Z"},
								{"bucketId": "3p-5h", "displayName": "Five Hour Limit Remaining", "refreshWindowLabel": "5h", "remainingFraction": 1.0, "resetTime": "2026-11-15T21:30:00Z"}
							]
						}
					]
				},
				"availableCredits": [{"creditType": "GOOGLE_ONE_AI", "creditAmount": 500}]
			}`), nil
		case r.URL.Host == "daily-cloudcode-pa.googleapis.com" && strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			return jsonResp(200, `{"cloudaicompanionProject":"aicode-consumers","paidTier":{"id":"g1-ultra-lite-tier","name":"Google AI Ultra"}}`), nil
		case r.URL.Host == "daily-cloudcode-pa.googleapis.com" && strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
			return jsonResp(200, `{
				"groups": [{
					"displayName": "Claude and GPT models",
					"buckets": [
						{"bucketId": "3p-weekly", "displayName": "Weekly Limit Remaining", "window": "weekly", "remainingFraction": 1.0, "resetTime": "2026-11-22T07:36:18Z"},
						{"bucketId": "3p-5h", "displayName": "Five Hour Limit Remaining", "window": "5h", "remainingFraction": 1.0, "resetTime": "2026-11-15T21:30:00Z"}
					]
				}]
			}`), nil
		case r.URL.Host == "daily-cloudcode-pa.googleapis.com" && strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			return jsonResp(200, `{
				"models": {
					"gemini-4-carbon-medium": {"quotaInfo": {"remainingFraction": 0.45, "resetTime": "2026-11-15T17:17:05Z"}}
				}
			}`), nil
		default:
			return jsonResp(404, `{}`), nil
		}
	})

	reg := NewRegistry()
	agy, _ := reg.Get("antigravity_oauth")

	// Case 1: Direct AICode HTTP 200 returns all 4 windows (Gemini 5h, Gemini Weekly, Claude/GPT 5h, Claude/GPT Weekly)
	res, err := agy.Fetch(context.Background(), model.EntryConfig{
		AuthMode:     "oauth",
		Secret:       "ya29.with-aicode-scope",
		AccountEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("direct aicode fetch failed: %v", err)
	}
	if res.PlanLabel != "AI Ultra · 500 Credits" {
		t.Fatalf("expected AI Ultra · 500 Credits, got %q", res.PlanLabel)
	}
	if len(res.Windows) != 4 {
		t.Fatalf("expected 4 windows including Gemini weekly, got %+v", res.Windows)
	}
	if res.Windows[0].Label != "Gemini (5h)" || res.Windows[0].RemainingPercent != 44.0 {
		t.Fatalf("expected Gemini (5h) 44%% remaining, got %+v", res.Windows[0])
	}
	if res.Windows[1].Label != "Gemini (周限额)" || res.Windows[1].RemainingPercent != 76.0 || res.Windows[1].WindowSeconds != 604800 {
		t.Fatalf("expected Gemini (周限额) 76%% remaining, got %+v", res.Windows[1])
	}
	if res.Windows[2].Label != "Claude / GPT (5h)" || res.Windows[3].Label != "Claude / GPT (周限额)" {
		t.Fatalf("expected Claude/GPT 5h & weekly windows at indices 2..3, got %+v", res.Windows[2:])
	}

	// Case 2: Legacy token without `auth/aicode` scope seamlessly falls back to local `agy -p /usage --output-format json` when timestamps align
	scopeInsufficient = true
	origRunner := agyCLIUsageRunner
	t.Cleanup(func() { agyCLIUsageRunner = origRunner })
	agyCLIUsageRunner = func(ctx context.Context) ([]byte, error) {
		return []byte(`{
			"status": "SUCCESS",
			"command": {
				"name": "usage",
				"data": {
					"groups": [
						{
							"name": "Gemini Models",
							"buckets": [
								{"id": "gemini-weekly", "name": "Weekly Limit Remaining", "window": "weekly", "remaining_fraction": 0.76, "reset_time": "2026-11-22T07:17:05Z"},
								{"id": "gemini-5h", "name": "Five Hour Limit Remaining", "window": "5h", "remaining_fraction": 0.44, "reset_time": "2026-11-15T17:17:05Z"}
							]
						},
						{
							"name": "Claude and GPT models",
							"buckets": [
								{"id": "3p-weekly", "name": "Weekly Limit Remaining", "window": "weekly", "remaining_fraction": 1.0, "reset_time": "2026-11-22T07:36:18Z"},
								{"id": "3p-5h", "name": "Five Hour Limit Remaining", "window": "5h", "remaining_fraction": 1.0, "reset_time": "2026-11-15T21:30:00Z"}
							]
						}
					]
				}
			}
		}`), nil
	}

	legacyRes, err := agy.Fetch(context.Background(), model.EntryConfig{
		AuthMode:     "oauth",
		Secret:       "ya29.legacy-without-aicode",
		AccountEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("legacy fallback fetch failed: %v", err)
	}
	if len(legacyRes.Windows) != 4 || legacyRes.Windows[1].Label != "Gemini (周限额)" || legacyRes.Windows[1].RemainingPercent != 76.0 {
		t.Fatalf("expected legacy CLI fallback to populate Gemini (周限额) 76%%, got %+v", legacyRes.Windows)
	}

	// Case 3: Mismatched local CLI account (different reset timestamps) is NOT mixed into another OAuth account's snapshot
	agyCLIUsageRunner = func(ctx context.Context) ([]byte, error) {
		return []byte(`{
			"status": "SUCCESS",
			"command": {
				"name": "usage",
				"data": {
					"groups": [{
						"name": "Gemini Models",
						"buckets": [
							{"id": "gemini-weekly", "name": "Weekly Limit Remaining", "window": "weekly", "remaining_fraction": 0.12, "reset_time": "2026-11-19T03:00:00Z"}
						]
					}]
				}
			}
		}`), nil
	}
	otherAcctRes, err := agy.Fetch(context.Background(), model.EntryConfig{
		AuthMode:     "oauth",
		Secret:       "ya29.legacy-other-acct",
		AccountEmail: "other@example.com",
	})
	if err != nil {
		t.Fatalf("mismatched account fetch failed: %v", err)
	}
	for _, w := range otherAcctRes.Windows {
		if w.Key == "gemini_weekly" {
			t.Fatalf("expected mismatched local CLI account not to leak gemini_weekly, got %+v", otherAcctRes.Windows)
		}
	}
}
