package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"xin.letra/sub-cost-view/server/internal/model"
)

// ============================================================================
// 3. DeepSeek API Balance
// ============================================================================

type deepseekProvider struct{}

func (p *deepseekProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "deepseek",
		Name:                  "DeepSeek API 余额",
		DefaultBadge:          "DS",
		DefaultKind:           model.KindBalance,
		DefaultRefreshSeconds: 600,
		DefaultDashboardURL:   "https://platform.deepseek.com/top_up",
		Description:           "通过 DeepSeek API Key 查询账户实时可用余额、充值余额与赠送金（支持 CNY / USD）。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "secret",
				Label:       "DeepSeek API Key",
				Type:        "secret",
				Required:    true,
				Placeholder: "sk-...",
			},
			{
				Key:     "currency",
				Label:   "优先展示币种",
				Type:    "select",
				Default: "CNY",
				Options: []model.Option{
					{Value: "CNY", Label: "人民币 (CNY ¥)"},
					{Value: "USD", Label: "美元 (USD $)"},
				},
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值（可选）",
				Type:        "number",
				Placeholder: "20",
			},
		},
	}
}

func (p *deepseekProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	key := strings.TrimSpace(cfg.Secret)
	if key == "" {
		return FetchResult{}, errors.New("未填写 DeepSeek API Key")
	}
	endpoint := strings.TrimSpace(cfg.BaseURL)
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/user/balance"
	}
	body, code, err := doJSONRequest(ctx, "GET", endpoint, map[string]string{
		"Authorization": "Bearer " + key,
	})
	if err != nil {
		return FetchResult{}, err
	}
	if code == 401 || code == 403 {
		return FetchResult{}, fmt.Errorf("DeepSeek API Key 无效 (HTTP %d)", code)
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("DeepSeek 返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	var payload struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string `json:"currency"`
			TotalBalance    string `json:"total_balance"`
			GrantedBalance  string `json:"granted_balance"`
			ToppedUpBalance string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return FetchResult{}, fmt.Errorf("解析 DeepSeek 响应失败: %w", err)
	}
	if len(payload.BalanceInfos) == 0 {
		return FetchResult{}, errors.New("DeepSeek 未返回余额条目")
	}

	wantCur := strings.ToUpper(coalesceStr(cfg.Currency, "CNY"))
	chosen := payload.BalanceInfos[0]
	for _, item := range payload.BalanceInfos {
		if strings.EqualFold(item.Currency, wantCur) {
			chosen = item
			break
		}
	}
	// If chosen has 0 balance and another currency has > 0, pick the non-zero currency automatically.
	total, _ := parseFlexibleFloat(chosen.TotalBalance)
	if total == 0 {
		for _, item := range payload.BalanceInfos {
			if v, _ := parseFlexibleFloat(item.TotalBalance); v > 0 {
				chosen = item
				total = v
				break
			}
		}
	}

	topped, _ := parseFlexibleFloat(chosen.ToppedUpBalance)
	granted, _ := parseFlexibleFloat(chosen.GrantedBalance)
	cur := strings.ToUpper(coalesceStr(chosen.Currency, "CNY"))
	sym := model.CurrencySymbol(cur)

	details := []model.KVDetail{
		{Label: "充值余额", Value: fmt.Sprintf("%s%.2f", sym, topped)},
	}
	if granted > 0 {
		details = append(details, model.KVDetail{Label: "赠金余额", Value: fmt.Sprintf("%s%.2f", sym, granted)})
	}

	isAvail := payload.IsAvailable
	bal := &model.BalanceInfo{
		Currency:     cur,
		Symbol:       sym,
		Available:    round2(total),
		LowThreshold: cfg.LowThreshold,
		IsAvailable:  &isAvail,
		Details:      details,
	}
	st, reason, primary := evaluateBalanceStatus(bal)
	return FetchResult{
		PlanLabel:     "API",
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Balance:       bal,
	}, nil
}

// ============================================================================
// 4. OpenRouter Credits / Key Quota
// ============================================================================

type openrouterProvider struct{}

func (p *openrouterProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "openrouter",
		Name:                  "OpenRouter Credits",
		DefaultBadge:          "OR",
		DefaultKind:           model.KindBalance,
		DefaultRefreshSeconds: 600,
		DefaultDashboardURL:   "https://openrouter.ai/settings/credits",
		Description:           "支持 Management Key（查账户总 Credits 剩余）或普通 API Key（查 Key 剩余额度与累计消耗）。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "secret",
				Label:       "OpenRouter Key (Management Key 或 API Key)",
				Type:        "secret",
				Required:    true,
				Placeholder: "sk-or-v1-...",
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值 ($)",
				Type:        "number",
				Placeholder: "5",
			},
		},
	}
}

func (p *openrouterProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	key := strings.TrimSpace(cfg.Secret)
	if key == "" {
		return FetchResult{}, errors.New("未填写 OpenRouter Key")
	}
	base := strings.TrimRight(coalesceStr(cfg.BaseURL, "https://openrouter.ai/api/v1"), "/")
	headers := map[string]string{"Authorization": "Bearer " + key}

	// 1. Try /credits first (works for Management Keys and many standard accounts)
	body, code, err := doJSONRequest(ctx, "GET", base+"/credits", headers)
	if err == nil && code >= 200 && code < 300 {
		var creditsDoc struct {
			Data *struct {
				TotalCredits float64 `json:"total_credits"`
				TotalUsage   float64 `json:"total_usage"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &creditsDoc) == nil && creditsDoc.Data != nil {
			avail := round2(creditsDoc.Data.TotalCredits - creditsDoc.Data.TotalUsage)
			used := round2(creditsDoc.Data.TotalUsage)
			limit := round2(creditsDoc.Data.TotalCredits)
			bal := &model.BalanceInfo{
				Currency:     "USD",
				Symbol:       "$",
				Available:    avail,
				Used:         &used,
				Limit:        &limit,
				LowThreshold: cfg.LowThreshold,
				Details: []model.KVDetail{
					{Label: "总充值", Value: fmt.Sprintf("$%.2f", limit)},
					{Label: "累计已用", Value: fmt.Sprintf("$%.2f", used)},
				},
			}
			st, reason, primary := evaluateBalanceStatus(bal)
			return FetchResult{
				PlanLabel:     "Credits",
				PrimaryMetric: primary,
				Status:        st,
				StatusReason:  reason,
				Balance:       bal,
			}, nil
		}
	}

	// 2. Fallback to /key (for scoped regular API keys)
	body, code, err = doJSONRequest(ctx, "GET", base+"/key", headers)
	if err != nil {
		return FetchResult{}, err
	}
	if code == 401 || code == 403 {
		return FetchResult{}, fmt.Errorf("OpenRouter Key 无效 (HTTP %d)", code)
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("OpenRouter 返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	var keyDoc struct {
		Data *struct {
			Label          string   `json:"label"`
			Usage          float64  `json:"usage"`
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			IsFreeTier     bool     `json:"is_free_tier"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &keyDoc); err != nil || keyDoc.Data == nil {
		return FetchResult{}, errors.New("解析 OpenRouter /key 响应失败")
	}

	used := round2(keyDoc.Data.Usage)
	if keyDoc.Data.LimitRemaining != nil {
		avail := round2(*keyDoc.Data.LimitRemaining)
		var lim *float64
		if keyDoc.Data.Limit != nil {
			l := round2(*keyDoc.Data.Limit)
			lim = &l
		}
		bal := &model.BalanceInfo{
			Currency:     "USD",
			Symbol:       "$",
			Available:    avail,
			Used:         &used,
			Limit:        lim,
			LowThreshold: cfg.LowThreshold,
			Details: []model.KVDetail{
				{Label: "Key 已用", Value: fmt.Sprintf("$%.2f", used)},
			},
		}
		st, reason, primary := evaluateBalanceStatus(bal)
		return FetchResult{
			PlanLabel:     coalesceStr(cfg.PlanLabel, "Key 限额"),
			PrimaryMetric: primary,
			Status:        st,
			StatusReason:  reason,
			Balance:       bal,
		}, nil
	}

	// Unlimited key: show usage cleanly without false zero-balance alarm
	bal := &model.BalanceInfo{
		Currency:  "USD",
		Symbol:    "$",
		Available: 0,
		Used:      &used,
		Details: []model.KVDetail{
			{Label: "Key 累计消耗", Value: fmt.Sprintf("$%.2f", used)},
			{Label: "限额类型", Value: "不限额 Key (建议换 Management Key 查总余额)"},
		},
	}
	return FetchResult{
		PlanLabel:     "PayGo",
		PrimaryMetric: fmt.Sprintf("已用 $%.2f", used),
		Status:        model.StatusOK,
		Balance:       bal,
	}, nil
}

// ============================================================================
// 5. Moonshot / Kimi Open Platform Balance
// ============================================================================

type moonshotProvider struct{}

func (p *moonshotProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "moonshot",
		Name:                  "Moonshot 开放平台余额",
		DefaultBadge:          "MSH",
		DefaultKind:           model.KindBalance,
		DefaultRefreshSeconds: 600,
		DefaultDashboardURL:   "https://platform.moonshot.cn/console/pay",
		Description:           "查询月之暗面 Moonshot 开放平台按量 API 可用余额、现金余额与代金券余额（Kimi 包月/会员订阅请选「Kimi 会员 / Kimi Code」）。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "secret",
				Label:       "Moonshot API Key",
				Type:        "secret",
				Required:    true,
				Placeholder: "sk-...",
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值 (¥)",
				Type:        "number",
				Placeholder: "15",
			},
		},
	}
}

func (p *moonshotProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	key := strings.TrimSpace(cfg.Secret)
	if key == "" {
		return FetchResult{}, errors.New("未填写 Moonshot API Key")
	}
	endpoint := coalesceStr(cfg.BaseURL, "https://api.moonshot.cn/v1/users/me/balance")
	body, code, err := doJSONRequest(ctx, "GET", endpoint, map[string]string{
		"Authorization": "Bearer " + key,
	})
	if err != nil {
		return FetchResult{}, err
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("Moonshot 返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	var payload struct {
		Data *struct {
			AvailableBalance float64 `json:"available_balance"`
			CashBalance      float64 `json:"cash_balance"`
			VoucherBalance   float64 `json:"voucher_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return FetchResult{}, errors.New("解析 Moonshot 余额响应失败")
	}

	avail := round2(payload.Data.AvailableBalance)
	bal := &model.BalanceInfo{
		Currency:     "CNY",
		Symbol:       "¥",
		Available:    avail,
		LowThreshold: cfg.LowThreshold,
		Details: []model.KVDetail{
			{Label: "现金余额", Value: fmt.Sprintf("¥%.2f", payload.Data.CashBalance)},
			{Label: "代金券", Value: fmt.Sprintf("¥%.2f", payload.Data.VoucherBalance)},
		},
	}
	st, reason, primary := evaluateBalanceStatus(bal)
	return FetchResult{
		PlanLabel:     "API",
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Balance:       bal,
	}, nil
}

// ============================================================================
// 6. SiliconFlow (硅基流动) Balance
// ============================================================================

type siliconflowProvider struct{}

func (p *siliconflowProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "siliconflow",
		Name:                  "SiliconFlow 硅基流动",
		DefaultBadge:          "SF",
		DefaultKind:           model.KindBalance,
		DefaultRefreshSeconds: 600,
		DefaultDashboardURL:   "https://cloud.siliconflow.cn/expensebill",
		Description:           "查询硅基流动账户总余额、充值余额与赠送余额。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "secret",
				Label:       "SiliconFlow API Key",
				Type:        "secret",
				Required:    true,
				Placeholder: "sk-...",
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值 (¥)",
				Type:        "number",
				Placeholder: "10",
			},
		},
	}
}

func (p *siliconflowProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	key := strings.TrimSpace(cfg.Secret)
	if key == "" {
		return FetchResult{}, errors.New("未填写 SiliconFlow API Key")
	}
	endpoint := coalesceStr(cfg.BaseURL, "https://api.siliconflow.cn/v1/user/info")
	body, code, err := doJSONRequest(ctx, "GET", endpoint, map[string]string{
		"Authorization": "Bearer " + key,
	})
	if err != nil {
		return FetchResult{}, err
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("SiliconFlow 返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	var payload struct {
		Data *struct {
			TotalBalance  string `json:"totalBalance"`
			ChargeBalance string `json:"chargeBalance"`
			Balance       string `json:"balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return FetchResult{}, errors.New("解析 SiliconFlow 响应失败")
	}

	total, _ := parseFlexibleFloat(payload.Data.TotalBalance)
	charge, _ := parseFlexibleFloat(payload.Data.ChargeBalance)
	gift, _ := parseFlexibleFloat(payload.Data.Balance)

	bal := &model.BalanceInfo{
		Currency:     "CNY",
		Symbol:       "¥",
		Available:    round2(total),
		LowThreshold: cfg.LowThreshold,
		Details: []model.KVDetail{
			{Label: "充值余额", Value: fmt.Sprintf("¥%.2f", charge)},
			{Label: "赠送余额", Value: fmt.Sprintf("¥%.2f", gift)},
		},
	}
	st, reason, primary := evaluateBalanceStatus(bal)
	return FetchResult{
		PlanLabel:     "API",
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Balance:       bal,
	}, nil
}

// ============================================================================
// 7. OpenAI-Compatible Billing / OneAPI / NewAPI Relay Station
// ============================================================================

type openaiBillingProvider struct{}

func (p *openaiBillingProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "openai_billing",
		Name:                  "兼容中转站 / NewAPI / OneAPI",
		DefaultBadge:          "API",
		DefaultKind:           model.KindBalance,
		DefaultRefreshSeconds: 600,
		Description:           "支持任意实现 /v1/dashboard/billing/subscription 与 usage 的中转站或兼容网关，通过 API Key 查询总额度与剩余额度。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "baseUrl",
				Label:       "站点根地址 (Base URL)",
				Type:        "text",
				Required:    true,
				Placeholder: "https://api.example.com",
				Help:        "无需带 /v1，程序会自动拼接 /v1/dashboard/billing/...",
			},
			{
				Key:         "secret",
				Label:       "API Key (sk-...)",
				Type:        "secret",
				Required:    true,
				Placeholder: "sk-...",
			},
			{
				Key:     "currency",
				Label:   "展示币种符号",
				Type:    "select",
				Default: "USD",
				Options: []model.Option{
					{Value: "USD", Label: "美元 ($)"},
					{Value: "CNY", Label: "人民币 (¥)"},
				},
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值（可选）",
				Type:        "number",
				Placeholder: "5",
			},
		},
	}
}

func (p *openaiBillingProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	key := strings.TrimSpace(cfg.Secret)
	if base == "" || key == "" {
		return FetchResult{}, errors.New("请填写站点地址 (Base URL) 与 API Key")
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/v1")

	headers := map[string]string{"Authorization": "Bearer " + key}
	subBody, code, err := doJSONRequest(ctx, "GET", base+"/v1/dashboard/billing/subscription", headers)
	if err != nil {
		return FetchResult{}, err
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("billing/subscription 返回 HTTP %d: %s", code, truncateErrBody(subBody))
	}

	var sub struct {
		HardLimitUSD       float64 `json:"hard_limit_usd"`
		SystemHardLimitUSD float64 `json:"system_hard_limit_usd"`
		AccessUntil        int64   `json:"access_until"`
		Plan               *struct {
			Title string `json:"title"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(subBody, &sub); err != nil {
		return FetchResult{}, fmt.Errorf("解析 subscription 失败: %w", err)
	}
	limit := sub.HardLimitUSD
	if limit <= 0 {
		limit = sub.SystemHardLimitUSD
	}

	now := time.Now().UTC()
	start := now.AddDate(0, 0, -90).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	usageURL := fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", base, start, end)
	usageBody, uCode, uErr := doJSONRequest(ctx, "GET", usageURL, headers)

	used := 0.0
	if uErr == nil && uCode >= 200 && uCode < 300 {
		var uDoc struct {
			TotalUsage float64 `json:"total_usage"` // in cents ($0.01)
		}
		if json.Unmarshal(usageBody, &uDoc) == nil {
			used = uDoc.TotalUsage / 100.0
		}
	}

	avail := round2(math.Max(0, limit-used))
	usedR := round2(used)
	limitR := round2(limit)
	cur := strings.ToUpper(coalesceStr(cfg.Currency, "USD"))
	sym := model.CurrencySymbol(cur)

	details := []model.KVDetail{
		{Label: "总额度", Value: fmt.Sprintf("%s%.2f", sym, limitR)},
		{Label: "近90天已用", Value: fmt.Sprintf("%s%.2f", sym, usedR)},
	}
	if sub.AccessUntil > 0 {
		if t := unixToTime(sub.AccessUntil); t != nil {
			details = append(details, model.KVDetail{Label: "有效期至", Value: t.Local().Format("2006-01-02")})
		}
	}

	bal := &model.BalanceInfo{
		Currency:     cur,
		Symbol:       sym,
		Available:    avail,
		Used:         &usedR,
		Limit:        &limitR,
		LowThreshold: cfg.LowThreshold,
		Details:      details,
	}
	st, reason, primary := evaluateBalanceStatus(bal)
	planLabel := "Relay"
	if sub.Plan != nil && sub.Plan.Title != "" {
		planLabel = sub.Plan.Title
	}
	if cfg.PlanLabel != "" {
		planLabel = cfg.PlanLabel
	}
	return FetchResult{
		PlanLabel:     planLabel,
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Balance:       bal,
	}, nil
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
