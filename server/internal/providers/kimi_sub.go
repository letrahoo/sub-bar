package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
)

// ============================================================================
// Kimi Subscription / Kimi Code Quota Provider (WeChat QR / Browser Device Auth)
// ============================================================================

type kimiSubscriptionProvider struct{}

func (p *kimiSubscriptionProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "kimi_oauth",
		Name:                  "Kimi 会员 / Kimi Code",
		DefaultBadge:          "KIMI",
		DefaultKind:           model.KindSubscription,
		SupportsOAuthFlow:     true,
		OAuthButtonLabel:      "打开 Kimi 授权页（支持微信扫码登录）",
		DefaultRefreshSeconds: 300,
		DefaultDashboardURL:   "https://www.kimi.com/code/console",
		Description:           "点击授权链接跳转 Kimi 官方登录页（原生支持微信扫码登录、手机号或网页已登录态一键确认），监控 5h 速率窗口与周/月会员配额及重置倒计时，支持自动续期；亦兼容手动填入 sk-kimi Key 或 kimi-auth 凭证。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:     "baseUrl",
				Label:   "账号所属站点",
				Type:    "select",
				Default: "",
				Options: []model.Option{
					{Value: "", Label: "中国大陆站 (kimi.com · 支持微信扫码/手机号)"},
					{Value: "https://api.kimi.ai", Label: "国际站 (kimi.ai · 海外账号)"},
				},
			},
			{
				Key:         "planLabel",
				Label:       "套餐标记（可选，留空自动识别）",
				Type:        "text",
				Placeholder: "如 Moderato / Allegretto / Kimi Code",
			},
			{
				Key:         "secret",
				Label:       "手动凭证（可选，使用上方微信扫码授权则留空）",
				Type:        "secret",
				Placeholder: "可选：sk-kimi-... 或浏览器 kimi-auth Cookie",
				Help:        "推荐直接点击上方按钮微信扫码一键接入（含自动续期）；仅在需要手动绑定独立 API Key 或 Cookie 时填写。",
			},
		},
	}
}

func (p *kimiSubscriptionProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	var tokenUpd *TokenUpdate
	refreshed := false
	if tokenNeedsProactiveRefresh(cfg) {
		if upd, err := RefreshKimiOAuthToken(ctx, cfg.RefreshToken, cfg.BaseURL, cfg.AccountID); err == nil {
			cfg.Secret = upd.AccessToken
			if upd.AccountID != "" && cfg.AccountID == "" {
				cfg.AccountID = upd.AccountID
			}
			tokenUpd = &upd
			refreshed = true
		} else if strings.TrimSpace(cfg.Secret) == "" {
			return FetchResult{}, err
		}
	}

	token, deviceID, err := resolveKimiCredentials(cfg)
	if err != nil {
		return FetchResult{}, err
	}

	codingURL, billingURL, webOrigin := resolveKimiEndpoints(cfg.BaseURL)

	doCodingCall := func(tok string) ([]byte, int, error) {
		headers := kimiClientHeaders(deviceID)
		headers["Authorization"] = "Bearer " + tok
		return doJSONRequest(ctx, http.MethodGet, codingURL, headers)
	}

	var body []byte
	var code int
	if codingURL != "" {
		body, code, err = doCodingCall(token)
		if err != nil {
			return FetchResult{}, err
		}
		if code == http.StatusUnauthorized && !refreshed && strings.TrimSpace(cfg.RefreshToken) != "" {
			upd, rErr := RefreshKimiOAuthToken(ctx, cfg.RefreshToken, cfg.BaseURL, deviceID)
			if rErr != nil {
				return FetchResult{}, rErr
			}
			tokenUpd = &upd
			token = upd.AccessToken
			body, code, err = doCodingCall(token)
			if err != nil {
				return FetchResult{}, err
			}
		}
		if code == http.StatusTooManyRequests {
			return FetchResult{}, errors.New("触发 Kimi 配额查询频率限制 (HTTP 429)，稍后自动重试")
		}
	}

	var parsed kimiParsedUsage
	if codingURL != "" && code >= 200 && code < 300 {
		parsed, _ = parseKimiUsagesPayload(body)
	}

	// Fallback to Kimi Web Connect-RPC BillingService/GetUsages when:
	// - user supplied a web `kimi-auth` session JWT (which may 401/403 on /coding/v1/usages), OR
	// - codingURL was explicitly pointed to GetUsages, OR
	// - /coding/v1/usages returned empty windows.
	if len(parsed.Windows) == 0 && billingURL != "" && !strings.HasPrefix(token, "sk-kimi-") {
		wBody, wCode, wErr := callKimiWebBilling(ctx, billingURL, webOrigin, token, []string{"FEATURE_CODING"})
		if wErr == nil && wCode >= 200 && wCode < 300 {
			if wp, pErr := parseKimiUsagesPayload(wBody); pErr == nil && len(wp.Windows) > 0 {
				parsed = wp
				code = wCode
			} else {
				// Also probe default empty scope in case the account holds general web membership quotas.
				wBody2, wCode2, wErr2 := callKimiWebBilling(ctx, billingURL, webOrigin, token, nil)
				if wErr2 == nil && wCode2 >= 200 && wCode2 < 300 {
					if wp2, pErr2 := parseKimiUsagesPayload(wBody2); pErr2 == nil && len(wp2.Windows) > 0 {
						parsed = wp2
						code = wCode2
					}
				}
			}
		} else if codingURL == "" && wErr != nil {
			return FetchResult{}, wErr
		} else if codingURL == "" {
			body = wBody
			code = wCode
		}
	}

	if len(parsed.Windows) == 0 {
		if code == http.StatusUnauthorized {
			return FetchResult{}, errors.New("Kimi 登录凭证已失效 (HTTP 401)，请点击编辑中的「打开 Kimi 授权页」重新扫码登录")
		}
		if code == http.StatusForbidden {
			detail := extractKimiErrorMessage(body)
			if detail != "" {
				return FetchResult{}, fmt.Errorf("当前 Kimi 账号暂无订阅配额权限 (HTTP 403: %s)", detail)
			}
			return FetchResult{}, errors.New("当前 Kimi 账号未开通 Kimi Code / 会员配额权限 (HTTP 403)")
		}
		if code < 200 || code >= 300 {
			return FetchResult{}, fmt.Errorf("Kimi 上游返回 HTTP %d: %s", code, coalesceStr(extractKimiErrorMessage(body), truncateErrBody(body)))
		}
		return FetchResult{}, errors.New("当前 Kimi 账号未检出有效订阅配额窗口（请确认该微信/Kimi 账号已开通会员或 Kimi Code 权益）")
	}

	warnPct := 75.0
	if cfg.WarnPercent != nil && *cfg.WarnPercent > 0 {
		warnPct = *cfg.WarnPercent
	}
	st, reason, primary := evaluateWindowsStatus(parsed.Windows, warnPct)

	plan := coalesceStr(cfg.PlanLabel, parsed.PlanLabel)
	if plan == "" {
		plan = "Kimi 订阅"
	}
	acct := coalesceStr(parsed.AccountIdentity, cfg.AccountEmail)

	return FetchResult{
		PlanLabel:     plan,
		AccountEmail:  acct,
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Windows:       parsed.Windows,
		TokenUpdate:   tokenUpd,
	}, nil
}

func resolveKimiEndpoints(baseURL string) (codingURL, billingURL, webOrigin string) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lower := strings.ToLower(trimmed)

	switch {
	case trimmed == "" || lower == "https://api.kimi.com" || lower == "https://www.kimi.com":
		return "https://api.kimi.com/coding/v1/usages",
			"https://www.kimi.com/apiv2/kimi.gateway.billing.v1.BillingService/GetUsages",
			"https://www.kimi.com"
	case lower == "https://api.kimi.ai" || lower == "https://www.kimi.ai":
		return "https://api.kimi.ai/coding/v1/usages",
			"https://www.kimi.ai/apiv2/kimi.gateway.billing.v1.BillingService/GetUsages",
			"https://www.kimi.ai"
	case strings.Contains(trimmed, "BillingService/GetUsages"):
		origin := "https://www.kimi.com"
		if idx := strings.Index(trimmed, "/apiv2/"); idx > 0 {
			origin = trimmed[:idx]
		}
		return "", trimmed, origin
	case strings.HasSuffix(lower, "/usages"):
		return trimmed, "", ""
	default:
		return trimmed + "/coding/v1/usages", "", ""
	}
}

func callKimiWebBilling(ctx context.Context, billingURL, webOrigin, token string, scopes []string) ([]byte, int, error) {
	payload := map[string]any{}
	if len(scopes) > 0 {
		payload["scope"] = scopes
	}
	headers := map[string]string{
		"Authorization":            "Bearer " + token,
		"connect-protocol-version": "1",
		"Origin":                   webOrigin,
		"Referer":                  webOrigin + "/",
		"User-Agent":               "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) SubBar/1.0",
	}
	return doJSONBodyRequest(ctx, http.MethodPost, billingURL, headers, payload)
}

func resolveKimiCredentials(cfg model.EntryConfig) (token string, deviceID string, err error) {
	deviceID = strings.TrimSpace(cfg.AccountID)
	tok := normalizeKimiSecret(cfg.Secret)
	if tok != "" {
		return tok, deviceID, nil
	}
	if cfg.AuthMode == "file" || strings.TrimSpace(cfg.CredentialFile) != "" {
		path, pErr := resolveSafeHomeJSONPath(cfg.CredentialFile, ".kimi/credentials/kimi-code.json")
		if pErr != nil {
			return "", "", pErr
		}
		raw, rErr := os.ReadFile(path)
		if rErr != nil {
			return "", "", fmt.Errorf("读取 Kimi 凭据文件失败: %w", rErr)
		}
		var doc struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", "", fmt.Errorf("解析 Kimi 凭据 JSON 失败: %w", err)
		}
		tok = normalizeKimiSecret(doc.AccessToken)
		if tok != "" {
			return tok, deviceID, nil
		}
	}
	return "", "", errors.New("尚未完成 Kimi 登录授权，请点击「打开 Kimi 授权页（支持微信扫码登录）」完成接入")
}

// normalizeKimiSecret cleans raw tokens, `Bearer ...` headers, or full browser Cookie strings containing `kimi-auth=...`.
func normalizeKimiSecret(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(s), "cookie:") {
		s = strings.TrimSpace(s[7:])
	}
	if strings.HasPrefix(strings.ToLower(s), "bearer ") {
		s = strings.TrimSpace(s[7:])
	}
	if strings.Contains(s, "kimi-auth=") {
		parts := strings.Split(s, ";")
		for _, part := range parts {
			kv := strings.TrimSpace(part)
			if strings.HasPrefix(kv, "kimi-auth=") {
				val := strings.TrimSpace(strings.TrimPrefix(kv, "kimi-auth="))
				val = strings.Trim(val, `"'`)
				if val != "" {
					return val
				}
			}
		}
	}
	return strings.Trim(s, `"'`)
}

type kimiParsedUsage struct {
	PlanLabel       string
	AccountIdentity string
	Windows         []model.QuotaWindow
}

type kimiQuotaDetail struct {
	Limit          any `json:"limit"`
	Used           any `json:"used"`
	Remaining      any `json:"remaining"`
	UsedRatio      any `json:"usedRatio"`
	UsedRatioSnake any `json:"used_ratio"`
	RemRatio       any `json:"remainingRatio"`
	RemRatioSnake  any `json:"remaining_ratio"`
	UsedPct        any `json:"usedPercent"`
	UsedPctSnake   any `json:"used_percent"`
	ResetTime      any `json:"resetTime"`
	ResetTimeSnake any `json:"reset_time"`
	ResetsAt       any `json:"resetsAt"`
	ResetAt        any `json:"resetAt"`
}

type kimiLimitItem struct {
	Window *struct {
		Duration int64  `json:"duration"`
		TimeUnit string `json:"timeUnit"`
	} `json:"window"`
	Detail *kimiQuotaDetail `json:"detail"`
}

func parseKimiUsagesPayload(raw []byte) (kimiParsedUsage, error) {
	var doc struct {
		User *struct {
			UserID     string `json:"userId"`
			Email      string `json:"email"`
			Phone      string `json:"phone"`
			Name       string `json:"name"`
			Nickname   string `json:"nickname"`
			Membership *struct {
				Level string `json:"level"`
				Name  string `json:"name"`
			} `json:"membership"`
		} `json:"user"`
		Membership *struct {
			Level string `json:"level"`
			Name  string `json:"name"`
		} `json:"membership"`
		Plan   string           `json:"plan"`
		Tier   string           `json:"tier"`
		Usage  *kimiQuotaDetail `json:"usage"`
		Limits []kimiLimitItem  `json:"limits"`
		Usages json.RawMessage  `json:"usages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return kimiParsedUsage{}, fmt.Errorf("解析 Kimi 配额 JSON 失败: %w", err)
	}

	var out kimiParsedUsage
	rawLevel := coalesceStr(doc.Plan, doc.Tier)
	if doc.Membership != nil {
		rawLevel = coalesceStr(doc.Membership.Name, coalesceStr(doc.Membership.Level, rawLevel))
	}
	if doc.User != nil {
		if doc.User.Membership != nil {
			rawLevel = coalesceStr(doc.User.Membership.Name, coalesceStr(doc.User.Membership.Level, rawLevel))
		}
		out.AccountIdentity = coalesceStr(doc.User.Email, coalesceStr(doc.User.Nickname, coalesceStr(doc.User.Name, doc.User.Phone)))
	}
	out.PlanLabel = normalizeKimiPlanLabel(rawLevel)

	seenKeys := make(map[string]bool)
	addWin := func(key, label string, sec int64, d *kimiQuotaDetail) {
		usedPct, disp, resetsAt, ok := evalKimiDetail(d)
		if !ok {
			return
		}
		if sec <= 0 {
			sec = inferKimiCycleSeconds(resetsAt)
		}
		if label == "" {
			label = kimiWindowLabel(sec)
		}
		if seenKeys[key] {
			return
		}
		seenKeys[key] = true
		out.Windows = append(out.Windows, makeWindow(key, label, usedPct, resetsAt, sec, disp))
	}

	// 1. Short-term rolling rate-limit windows (e.g. 300 min = 5h window)
	for idx, lim := range doc.Limits {
		sec := kimiDurationSeconds(lim.Window)
		key := fmt.Sprintf("limit_%ds_%d", sec, idx)
		if sec == 18000 {
			key = "limit_5h"
		}
		addWin(key, kimiWindowLabel(sec), sec, lim.Detail)
	}

	// 2. Primary subscription cycle quota (`usage`)
	if doc.Usage != nil {
		_, _, resetsAt, ok := evalKimiDetail(doc.Usage)
		if ok {
			sec := inferKimiCycleSeconds(resetsAt)
			lbl := "周订阅配额"
			key := "weekly"
			if sec >= 20*86400 {
				lbl = "月度订阅配额"
				key = "monthly"
			}
			addWin(key, lbl, sec, doc.Usage)
		}
	}

	// 3. Polymorphic `usages` field: array (Connect-RPC GetUsages) OR map (pool ratios)
	trimmedUsages := strings.TrimSpace(string(doc.Usages))
	if strings.HasPrefix(trimmedUsages, "[") {
		var arr []struct {
			Scope  string           `json:"scope"`
			Detail *kimiQuotaDetail `json:"detail"`
			Limits []kimiLimitItem  `json:"limits"`
		}
		if err := json.Unmarshal(doc.Usages, &arr); err == nil {
			for i, item := range arr {
				scopePrefix := normalizeKimiScopePrefix(item.Scope, len(arr) > 1)
				for j, lim := range item.Limits {
					sec := kimiDurationSeconds(lim.Window)
					key := fmt.Sprintf("%slimit_%ds_%d_%d", item.Scope, sec, i, j)
					if sec == 18000 && scopePrefix == "" {
						key = "limit_5h"
					}
					addWin(key, scopePrefix+kimiWindowLabel(sec), sec, lim.Detail)
				}
				if item.Detail != nil {
					_, _, resetsAt, ok := evalKimiDetail(item.Detail)
					if ok {
						sec := inferKimiCycleSeconds(resetsAt)
						lbl := "周订阅配额"
						if sec >= 20*86400 {
							lbl = "月度订阅配额"
						}
						addWin(fmt.Sprintf("scope_%s_%d", strings.ToLower(item.Scope), i), scopePrefix+lbl, sec, item.Detail)
					}
				}
			}
		}
	} else if strings.HasPrefix(trimmedUsages, "{") {
		var poolMap map[string]kimiQuotaDetail
		if err := json.Unmarshal(doc.Usages, &poolMap); err == nil {
			keys := make([]string, 0, len(poolMap))
			for k := range poolMap {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				d := poolMap[k]
				key, lbl, sec := mapKimiPoolKey(k)
				addWin(key, lbl, sec, &d)
			}
		}
	}

	// Sort shorter windows first (5h -> 24h -> 7d -> 30d) for consistent top-to-bottom scan.
	sort.SliceStable(out.Windows, func(i, j int) bool {
		si, sj := out.Windows[i].WindowSeconds, out.Windows[j].WindowSeconds
		if si > 0 && sj > 0 && si != sj {
			return si < sj
		}
		return false
	})

	return out, nil
}

func evalKimiDetail(d *kimiQuotaDetail) (usedPct float64, usedDisplay string, resetsAt *time.Time, ok bool) {
	if d == nil {
		return 0, "", nil, false
	}
	for _, cand := range []any{d.ResetTime, d.ResetTimeSnake, d.ResetsAt, d.ResetAt} {
		if t := parseFlexibleTime(cand); t != nil {
			resetsAt = t
			break
		}
	}

	lim, hasLim := parseFlexibleFloat(d.Limit)
	used, hasUsed := parseFlexibleFloat(d.Used)
	rem, hasRem := parseFlexibleFloat(d.Remaining)

	if hasLim && lim > 0 {
		if !hasUsed && hasRem {
			used = math.Max(0, lim-rem)
		} else if !hasUsed && !hasRem {
			used = 0
		}
		usedPct = clampPercent((used / lim) * 100.0)
		if lim != 100 && math.Abs(lim-math.Round(lim)) < 1e-6 {
			usedDisplay = fmt.Sprintf("已用 %.0f/%.0f", used, lim)
		} else {
			usedDisplay = fmt.Sprintf("已用 %.0f%%", usedPct)
		}
		return usedPct, usedDisplay, resetsAt, true
	}

	if p, pOK := firstFlexibleFloat(d.UsedPct, d.UsedPctSnake); pOK {
		usedPct = clampPercent(p)
		return usedPct, fmt.Sprintf("已用 %.0f%%", usedPct), resetsAt, true
	}
	if r, rOK := firstFlexibleFloat(d.UsedRatio, d.UsedRatioSnake); rOK {
		usedPct = clampPercent(r * 100.0)
		return usedPct, fmt.Sprintf("已用 %.0f%%", usedPct), resetsAt, true
	}
	if r, rOK := firstFlexibleFloat(d.RemRatio, d.RemRatioSnake); rOK {
		usedPct = clampPercent((1.0 - r) * 100.0)
		return usedPct, fmt.Sprintf("已用 %.0f%%", usedPct), resetsAt, true
	}
	return 0, "", nil, false
}

func firstFlexibleFloat(vals ...any) (float64, bool) {
	for _, v := range vals {
		if v == nil {
			continue
		}
		if f, ok := parseFlexibleFloat(v); ok {
			return f, true
		}
	}
	return 0, false
}

func kimiDurationSeconds(w *struct {
	Duration int64  `json:"duration"`
	TimeUnit string `json:"timeUnit"`
}) int64 {
	if w == nil || w.Duration <= 0 {
		return 18000
	}
	unit := strings.ToUpper(strings.TrimSpace(w.TimeUnit))
	switch {
	case strings.Contains(unit, "SECOND"):
		return w.Duration
	case strings.Contains(unit, "MINUTE"):
		return w.Duration * 60
	case strings.Contains(unit, "HOUR"):
		return w.Duration * 3600
	case strings.Contains(unit, "DAY"):
		return w.Duration * 86400
	default:
		return w.Duration * 60
	}
}

func inferKimiCycleSeconds(resetsAt *time.Time) int64 {
	if resetsAt != nil {
		until := time.Until(*resetsAt)
		if until > 8*24*time.Hour {
			return 30 * 86400
		}
	}
	return 7 * 86400
}

func kimiWindowLabel(sec int64) string {
	switch {
	case sec == 18000:
		return "5小时窗口"
	case sec == 86400:
		return "24小时窗口"
	case sec == 604800:
		return "周订阅配额"
	case sec >= 20*86400:
		return "月度订阅配额"
	case sec > 0 && sec%3600 == 0:
		return fmt.Sprintf("%d小时窗口", sec/3600)
	case sec > 0 && sec%60 == 0:
		return fmt.Sprintf("%d分钟窗口", sec/60)
	default:
		return "滚动配额"
	}
}

func normalizeKimiScopePrefix(scope string, multi bool) string {
	s := strings.ToUpper(strings.TrimSpace(scope))
	switch s {
	case "", "FEATURE_CODING":
		if multi {
			return "Coding "
		}
		return ""
	case "FEATURE_RESEARCH":
		return "深度研究 "
	case "FEATURE_SEARCH":
		return "搜索 "
	case "FEATURE_CHAT":
		return "对话 "
	default:
		clean := strings.TrimPrefix(s, "FEATURE_")
		if clean != "" && multi {
			return clean + " "
		}
		return ""
	}
}

func mapKimiPoolKey(rawKey string) (key, label string, sec int64) {
	k := strings.ToLower(strings.TrimSpace(rawKey))
	switch k {
	case "limit_5h", "five_hour", "5h":
		return "limit_5h", "5小时窗口", 18000
	case "limit_24h", "daily", "1d":
		return "limit_24h", "24小时窗口", 86400
	case "limit_7d", "limit_week", "weekly", "7d":
		return "weekly", "周订阅配额", 604800
	case "limit_month_code", "month_code":
		return "month_code", "Kimi Code 月配额", 30 * 86400
	case "limit_month_total", "monthly", "month_total":
		return "month_total", "月度总配额", 30 * 86400
	default:
		return k, rawKey, 604800
	}
}

func normalizeKimiPlanLabel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	upper := strings.ToUpper(s)
	switch upper {
	case "LEVEL_BASIC", "BASIC":
		return "Allegretto"
	case "LEVEL_INTERMEDIATE", "INTERMEDIATE":
		return "Moderato"
	case "LEVEL_ADVANCED", "ADVANCED":
		return "Vivace"
	case "LEVEL_PRO", "PRO":
		return "Pro"
	case "LEVEL_MAX", "LEVEL_ULTRA", "ULTRA", "MAX":
		return "Ultra"
	case "LEVEL_FREE", "FREE":
		return "Free"
	}
	clean := strings.TrimPrefix(upper, "LEVEL_")
	if clean != upper {
		return clean
	}
	return s
}

func extractKimiErrorMessage(raw []byte) string {
	var doc struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	if strings.TrimSpace(doc.Message) != "" {
		return strings.TrimSpace(doc.Message)
	}
	switch e := doc.Error.(type) {
	case string:
		return strings.TrimSpace(e)
	case map[string]any:
		if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
			return strings.TrimSpace(m)
		}
		if t, ok := e["type"].(string); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return ""
}
