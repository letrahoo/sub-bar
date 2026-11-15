package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"xin.letra/sub-cost-view/server/internal/model"
)

// ============================================================================
// 1. OpenAI Codex / ChatGPT Rolling Quota (wham/usage)
// ============================================================================

type codexProvider struct{}

func (p *codexProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "codex_oauth",
		Name:                  "OpenAI Codex / ChatGPT",
		DefaultBadge:          "CDX",
		DefaultKind:           model.KindSubscription,
		SupportsOAuthFlow:     true,
		OAuthButtonLabel:      "打开 OpenAI 授权链接（一键接入 Codex）",
		DefaultRefreshSeconds: 300,
		DefaultDashboardURL:   "https://chatgpt.com/codex/settings/usage",
		Description:           "通过浏览器点击授权链接一键接入 ChatGPT Plus / Pro / Team，自动监控 Codex 5h 与 7d 滚动配额及重置倒计时，支持后台自动续期与多账号。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "planLabel",
				Label:       "套餐标记（可选，留空自动识别）",
				Type:        "text",
				Placeholder: "如 PRO / PLUS / TEAM",
			},
			{
				Key:         "accountId",
				Label:       "指定工作区 ID（可选，默认自动提取）",
				Type:        "text",
				Placeholder: "多工作区账号可指定 chatgpt-account-id",
			},
		},
	}
}

func (p *codexProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	var tokenUpd *TokenUpdate
	refreshed := false
	if tokenNeedsProactiveRefresh(cfg) {
		if upd, err := RefreshOAuthToken(ctx, "codex_oauth", cfg.RefreshToken); err == nil {
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

	token, accountID, err := resolveCodexCredentials(cfg)
	if err != nil {
		return FetchResult{}, err
	}

	endpoint := strings.TrimSpace(cfg.BaseURL)
	if endpoint == "" {
		endpoint = "https://chatgpt.com/backend-api/wham/usage"
	}
	doCall := func(tok, aid string) ([]byte, int, error) {
		headers := map[string]string{
			"Authorization": "Bearer " + tok,
			"User-Agent":    "codex-cli/0.1.0",
		}
		if aid != "" {
			headers["ChatGPT-Account-Id"] = aid
		}
		return doJSONRequest(ctx, "GET", endpoint, headers)
	}

	body, code, err := doCall(token, accountID)
	if err != nil {
		return FetchResult{}, err
	}
	if code == 401 && !refreshed && strings.TrimSpace(cfg.RefreshToken) != "" {
		upd, rErr := RefreshOAuthToken(ctx, "codex_oauth", cfg.RefreshToken)
		if rErr != nil {
			return FetchResult{}, rErr
		}
		tokenUpd = &upd
		if accountID == "" && upd.AccountID != "" {
			accountID = upd.AccountID
		}
		body, code, err = doCall(upd.AccessToken, accountID)
		if err != nil {
			return FetchResult{}, err
		}
	}
	if code == 401 || code == 403 {
		return FetchResult{}, fmt.Errorf("认证已失效 (HTTP %d)，请点击编辑中的「打开 OpenAI 授权链接」重新授权", code)
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("上游返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	var payload struct {
		PlanType  string `json:"plan_type"`
		RateLimit *struct {
			Allowed         bool            `json:"allowed"`
			LimitReached    bool            `json:"limit_reached"`
			PrimaryWindow   *codexRawWindow `json:"primary_window"`
			SecondaryWindow *codexRawWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		CodeReviewRateLimit *struct {
			PrimaryWindow *codexRawWindow `json:"primary_window"`
		} `json:"code_review_rate_limit"`
		Credits *struct {
			HasCredits bool `json:"has_credits"`
			Unlimited  bool `json:"unlimited"`
			Balance    any  `json:"balance"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return FetchResult{}, fmt.Errorf("解析 Codex 配额响应失败: %w", err)
	}

	var windows []model.QuotaWindow
	if payload.RateLimit != nil {
		if payload.RateLimit.PrimaryWindow != nil {
			windows = append(windows, convertCodexWindow("primary", "", payload.RateLimit.PrimaryWindow))
		}
		if payload.RateLimit.SecondaryWindow != nil {
			windows = append(windows, convertCodexWindow("secondary", "", payload.RateLimit.SecondaryWindow))
		}
	}
	if payload.CodeReviewRateLimit != nil && payload.CodeReviewRateLimit.PrimaryWindow != nil {
		windows = append(windows, convertCodexWindow("review", "Code Review ", payload.CodeReviewRateLimit.PrimaryWindow))
	}
	// Put shorter window (5h) first, then weekly (7d), matching CodexBar's intuitive top-to-bottom scan.
	if len(windows) >= 2 && windows[0].WindowSeconds > windows[1].WindowSeconds && windows[1].WindowSeconds > 0 {
		windows[0], windows[1] = windows[1], windows[0]
	}

	warnPct := 75.0
	if cfg.WarnPercent != nil && *cfg.WarnPercent > 0 {
		warnPct = *cfg.WarnPercent
	}
	st, reason, primary := evaluateWindowsStatus(windows, warnPct)
	if payload.RateLimit != nil && payload.RateLimit.LimitReached {
		st = model.StatusCritical
		if reason == "" {
			reason = "已触发速率限制"
		}
	}

	var bal *model.BalanceInfo
	if payload.Credits != nil && payload.Credits.HasCredits {
		if bVal, ok := parseFlexibleFloat(payload.Credits.Balance); ok {
			bal = &model.BalanceInfo{
				Currency:  "USD",
				Symbol:    "$",
				Available: bVal,
				Details:   []model.KVDetail{{Label: "附加 Credits", Value: fmt.Sprintf("$%.2f", bVal)}},
			}
		}
	}

	plan := strings.ToUpper(strings.TrimSpace(payload.PlanType))
	if cfg.PlanLabel != "" {
		plan = cfg.PlanLabel
	}
	return FetchResult{
		PlanLabel:     plan,
		AccountEmail:  cfg.AccountEmail,
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Windows:       windows,
		Balance:       bal,
		TokenUpdate:   tokenUpd,
	}, nil
}

type codexRawWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

func convertCodexWindow(keyPrefix, labelPrefix string, w *codexRawWindow) model.QuotaWindow {
	var resetsAt *time.Time
	if w.ResetAt > 0 {
		resetsAt = unixToTime(w.ResetAt)
	} else if w.ResetAfterSeconds > 0 {
		t := time.Now().UTC().Add(time.Duration(w.ResetAfterSeconds) * time.Second)
		resetsAt = &t
	}
	label := labelPrefix + windowDurationLabel(w.LimitWindowSeconds)
	return makeWindow(keyPrefix, label, w.UsedPercent, resetsAt, w.LimitWindowSeconds, fmt.Sprintf("已用 %.0f%%", w.UsedPercent))
}

func windowDurationLabel(sec int64) string {
	switch {
	case sec == 18000:
		return "5小时窗口"
	case sec == 86400:
		return "24小时窗口"
	case sec == 604800:
		return "7天周限额"
	case sec > 0 && sec%86400 == 0:
		return fmt.Sprintf("%d天窗口", sec/86400)
	case sec > 0 && sec%3600 == 0:
		return fmt.Sprintf("%d小时窗口", sec/3600)
	default:
		return "滚动配额"
	}
}

func resolveCodexCredentials(cfg model.EntryConfig) (token string, accountID string, err error) {
	accountID = strings.TrimSpace(cfg.AccountID)
	if cfg.AuthMode == "oauth" || cfg.AuthMode == "token" || (cfg.AuthMode == "" && strings.TrimSpace(cfg.Secret) != "") {
		tok := strings.TrimSpace(cfg.Secret)
		if tok == "" {
			return "", "", errors.New("尚未完成 OpenAI 登录授权，请点击「打开 OpenAI 授权链接」完成接入")
		}
		if accountID == "" {
			_, accountID = extractJWTIdentity(tok)
		}
		return tok, accountID, nil
	}
	if cfg.AuthMode == "" && strings.TrimSpace(cfg.CredentialFile) == "" {
		return "", "", errors.New("尚未完成 OpenAI 登录授权，请点击「打开 OpenAI 授权链接」完成接入")
	}

	path, err := resolveSafeHomeJSONPath(cfg.CredentialFile, ".codex/auth.json")
	if err != nil {
		return "", "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("读取凭据文件失败: %w", err)
	}
	var doc struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
		Tokens      *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", fmt.Errorf("解析凭据 JSON 失败: %w", err)
	}
	if doc.Tokens != nil && doc.Tokens.AccessToken != "" {
		token = doc.Tokens.AccessToken
		if accountID == "" {
			accountID = doc.Tokens.AccountID
		}
	} else {
		token = doc.AccessToken
		if accountID == "" {
			accountID = doc.AccountID
		}
	}
	if strings.TrimSpace(token) == "" {
		return "", "", errors.New("凭据文件中未找到有效的 access_token")
	}
	return strings.TrimSpace(token), strings.TrimSpace(accountID), nil
}

// ============================================================================
// 2. Claude Code / Claude Pro & Max Rolling Quota (api/oauth/usage)
// ============================================================================

type claudeProvider struct{}

func (p *claudeProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "claude_oauth",
		Name:                  "Claude Pro / Max",
		DefaultBadge:          "CLD",
		DefaultKind:           model.KindSubscription,
		SupportsOAuthFlow:     true,
		OAuthButtonLabel:      "打开 Claude 授权链接（一键接入 Claude）",
		DefaultRefreshSeconds: 300,
		DefaultDashboardURL:   "https://claude.ai/settings/usage",
		Description:           "通过浏览器点击授权链接一键接入 Claude Pro / Max，监控 5h 会话窗口、7d 周限额、Sonnet/Opus 限额与 Extra Usage，自动续期令牌。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "planLabel",
				Label:       "套餐标记（可选，留空自动识别）",
				Type:        "text",
				Placeholder: "如 Max 5x / Pro",
			},
		},
	}
}

func (p *claudeProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	var tokenUpd *TokenUpdate
	refreshed := false
	if tokenNeedsProactiveRefresh(cfg) {
		if upd, err := RefreshOAuthToken(ctx, "claude_oauth", cfg.RefreshToken); err == nil {
			cfg.Secret = upd.AccessToken
			tokenUpd = &upd
			refreshed = true
		} else if strings.TrimSpace(cfg.Secret) == "" {
			return FetchResult{}, err
		}
	}

	token, subType, err := resolveClaudeToken(ctx, cfg)
	if err != nil {
		return FetchResult{}, err
	}

	endpoint := strings.TrimSpace(cfg.BaseURL)
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/api/oauth/usage"
	}
	doCall := func(tok string) ([]byte, int, error) {
		headers := map[string]string{
			"Authorization":  "Bearer " + tok,
			"anthropic-beta": "oauth-2025-04-20",
			"User-Agent":     "claude-code/2.1.0",
		}
		return doJSONRequest(ctx, "GET", endpoint, headers)
	}

	body, code, err := doCall(token)
	if err != nil {
		return FetchResult{}, err
	}
	if code == 401 && !refreshed && strings.TrimSpace(cfg.RefreshToken) != "" {
		upd, rErr := RefreshOAuthToken(ctx, "claude_oauth", cfg.RefreshToken)
		if rErr != nil {
			return FetchResult{}, rErr
		}
		tokenUpd = &upd
		body, code, err = doCall(upd.AccessToken)
		if err != nil {
			return FetchResult{}, err
		}
	}
	if code == 401 || code == 403 {
		return FetchResult{}, fmt.Errorf("Claude 认证已失效 (HTTP %d)，请点击编辑中的「打开 Claude 授权链接」重新授权", code)
	}
	if code == 429 {
		return FetchResult{}, errors.New("触发上游配额查询频率限制 (HTTP 429)，稍后自动重试")
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("上游返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	type claudeBucket struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	var payload struct {
		FiveHour       *claudeBucket `json:"five_hour"`
		SevenDay       *claudeBucket `json:"seven_day"`
		SevenDaySonnet *claudeBucket `json:"seven_day_sonnet"`
		SevenDayOpus   *claudeBucket `json:"seven_day_opus"`
		ExtraUsage     *struct {
			IsEnabled    bool     `json:"is_enabled"`
			MonthlyLimit *float64 `json:"monthly_limit"`
			UsedCredits  *float64 `json:"used_credits"`
			Utilization  *float64 `json:"utilization"`
			Currency     string   `json:"currency"`
		} `json:"extra_usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return FetchResult{}, fmt.Errorf("解析 Claude 配额响应失败: %w", err)
	}

	var windows []model.QuotaWindow
	addBucket := func(key, label string, sec int64, b *claudeBucket) {
		if b == nil || b.Utilization == nil {
			return
		}
		resetsAt := parseFlexibleTime(b.ResetsAt)
		windows = append(windows, makeWindow(key, label, *b.Utilization, resetsAt, sec, fmt.Sprintf("已用 %.0f%%", *b.Utilization)))
	}
	addBucket("5h", "5小时窗口", 18000, payload.FiveHour)
	addBucket("7d", "7天总额度", 604800, payload.SevenDay)
	addBucket("7d_sonnet", "Sonnet 周限", 604800, payload.SevenDaySonnet)
	addBucket("7d_opus", "Opus 周限", 604800, payload.SevenDayOpus)

	var bal *model.BalanceInfo
	if payload.ExtraUsage != nil && payload.ExtraUsage.IsEnabled && payload.ExtraUsage.UsedCredits != nil {
		// Claude API reports extra_usage credits in cents (e.g. 1250 = $12.50) or dollars depending on tier; handle cleanly.
		used := *payload.ExtraUsage.UsedCredits
		var limit *float64
		if payload.ExtraUsage.MonthlyLimit != nil {
			l := *payload.ExtraUsage.MonthlyLimit
			// When utilization matches cents scale (e.g. used=500, limit=5000, utilization=10%), convert cents -> USD if both >= 100 and utilization aligns.
			if l >= 100 && payload.ExtraUsage.Utilization != nil && *payload.ExtraUsage.Utilization > 0 {
				used = used / 100.0
				l = l / 100.0
			}
			limit = &l
		}
		avail := 0.0
		if limit != nil && *limit > used {
			avail = *limit - used
		}
		cur := strings.ToUpper(coalesceStr(payload.ExtraUsage.Currency, "USD"))
		bal = &model.BalanceInfo{
			Currency:  cur,
			Symbol:    model.CurrencySymbol(cur),
			Available: avail,
			Used:      &used,
			Limit:     limit,
			Details: []model.KVDetail{
				{Label: "Extra 已用", Value: fmt.Sprintf("%s%.2f", model.CurrencySymbol(cur), used)},
			},
		}
	}

	warnPct := 75.0
	if cfg.WarnPercent != nil && *cfg.WarnPercent > 0 {
		warnPct = *cfg.WarnPercent
	}
	st, reason, primary := evaluateWindowsStatus(windows, warnPct)
	plan := coalesceStr(cfg.PlanLabel, strings.ToUpper(subType))
	if plan == "" {
		plan = "OAuth"
	}

	return FetchResult{
		PlanLabel:     plan,
		AccountEmail:  cfg.AccountEmail,
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Windows:       windows,
		Balance:       bal,
		TokenUpdate:   tokenUpd,
	}, nil
}

func resolveClaudeToken(ctx context.Context, cfg model.EntryConfig) (token string, subType string, err error) {
	mode := cfg.AuthMode
	if mode == "" {
		if strings.TrimSpace(cfg.Secret) != "" {
			mode = "oauth"
		} else if strings.TrimSpace(cfg.CredentialFile) != "" {
			mode = "file"
		} else {
			return "", "", errors.New("尚未完成 Claude 登录授权，请点击「打开 Claude 授权链接」完成接入")
		}
	}

	switch mode {
	case "oauth", "token":
		tok := strings.TrimSpace(cfg.Secret)
		if tok == "" {
			return "", "", errors.New("尚未完成 Claude 登录授权，请点击「打开 Claude 授权链接」完成接入")
		}
		return tok, "", nil

	case "keychain":
		if runtime.GOOS != "darwin" {
			return "", "", errors.New("当前系统不支持 macOS Keychain，请改用文件或 Token 模式")
		}
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, "/usr/bin/security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
		if err != nil {
			return "", "", errors.New("未在 macOS 钥匙串中找到 Claude Code-credentials，请确认已登录 Claude Code 或改用 Token 模式")
		}
		return parseClaudeCredentialBlob(out)

	case "file":
		path, err := resolveSafeHomeJSONPath(cfg.CredentialFile, ".claude/.credentials.json")
		if err != nil {
			return "", "", err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("读取 Claude 凭据文件失败: %w", err)
		}
		return parseClaudeCredentialBlob(raw)

	default:
		return "", "", fmt.Errorf("未知凭据模式: %s", mode)
	}
}

func parseClaudeCredentialBlob(raw []byte) (string, string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "sk-ant-") {
		return trimmed, "", nil
	}
	var doc struct {
		AccessToken   string `json:"access_token"`
		ClaudeAiOauth *struct {
			AccessToken      string `json:"accessToken"`
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return "", "", fmt.Errorf("解析 Claude 凭据 JSON 失败: %w", err)
	}
	if doc.ClaudeAiOauth != nil && doc.ClaudeAiOauth.AccessToken != "" {
		return strings.TrimSpace(doc.ClaudeAiOauth.AccessToken), doc.ClaudeAiOauth.SubscriptionType, nil
	}
	if doc.AccessToken != "" {
		return strings.TrimSpace(doc.AccessToken), "", nil
	}
	return "", "", errors.New("Claude 凭据中未找到 accessToken")
}

func coalesceStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}
