package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/letrahoo/sub-cost-view/server/internal/model"
)

// ============================================================================
// 8. Universal Custom HTTP JSON Probe (Supports ANY REST API!)
// ============================================================================

type customHTTPProvider struct{}

func (p *customHTTPProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "custom_http",
		Name:                  "自定义 HTTP JSON 接口",
		DefaultBadge:          "HTTP",
		DefaultKind:           model.KindBalance,
		SupportsKindSwitch:    true,
		DefaultRefreshSeconds: 600,
		Description:           "通用扩展入口：填写任意第三方订阅或 API 服务的 JSON 接口地址与字段路径（如 data.balance 或 data.used_percent），零代码接入任何私有/冷门服务。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "baseUrl",
				Label:       "请求 URL",
				Type:        "text",
				Required:    true,
				Placeholder: "https://api.example.com/v1/account",
			},
			{
				Key:     "method",
				Label:   "HTTP 方法",
				Type:    "select",
				Default: "GET",
				Options: []model.Option{
					{Value: "GET", Label: "GET"},
					{Value: "POST", Label: "POST"},
				},
			},
			{
				Key:         "authHeader",
				Label:       "认证 Header 名称",
				Type:        "text",
				Default:     "Authorization",
				Placeholder: "Authorization 或 X-API-Key 或 Cookie",
			},
			{
				Key:         "authPrefix",
				Label:       "认证值前缀（可选）",
				Type:        "text",
				Default:     "Bearer ",
				Placeholder: "Bearer （若填 Cookie 或裸 Key 可留空）",
			},
			{
				Key:         "secret",
				Label:       "密钥 / Token / Cookie 值",
				Type:        "secret",
				Placeholder: "留空则匿名请求",
			},
			{
				Key:         "balancePath",
				Label:       "余额 JSON 路径（余额模式）",
				Type:        "text",
				Placeholder: "如 data.available_balance 或 data.quota",
			},
			{
				Key:         "usedPercentPath",
				Label:       "已用百分比 JSON 路径（订阅模式）",
				Type:        "text",
				Placeholder: "如 rate_limit.used_percent（也可填 usedPath + limitPath 自动算百分比）",
			},
			{
				Key:         "usedPath",
				Label:       "已用量 JSON 路径（可选）",
				Type:        "text",
				Placeholder: "如 data.used",
			},
			{
				Key:         "limitPath",
				Label:       "总额度 JSON 路径（可选）",
				Type:        "text",
				Placeholder: "如 data.limit",
			},
			{
				Key:         "resetAtPath",
				Label:       "重置时间 JSON 路径（可选）",
				Type:        "text",
				Placeholder: "如 data.reset_at（支持秒/毫秒时间戳或 ISO 日期）",
			},
			{
				Key:         "scale",
				Label:       "数值倍率（默认 1）",
				Type:        "number",
				Default:     "1",
				Placeholder: "1（如分转元填 0.01，NewAPI quota 转美元填 0.000002）",
			},
			{
				Key:     "currency",
				Label:   "币种（余额模式）",
				Type:    "select",
				Default: "USD",
				Options: []model.Option{
					{Value: "CNY", Label: "人民币 (CNY ¥)"},
					{Value: "USD", Label: "美元 (USD $)"},
				},
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值（可选）",
				Type:        "number",
				Placeholder: "10",
			},
		},
	}
}

func (p *customHTTPProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	url := strings.TrimSpace(cfg.BaseURL)
	if url == "" {
		return FetchResult{}, errors.New("未填写自定义接口 URL")
	}
	headers := make(map[string]string)
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if strings.TrimSpace(cfg.Secret) != "" {
		hdr := strings.TrimSpace(cfg.AuthHeader)
		if hdr == "" {
			hdr = "Authorization"
		}
		prefix := cfg.AuthPrefix
		if cfg.AuthHeader == "" && prefix == "" {
			prefix = "Bearer "
		}
		headers[hdr] = prefix + strings.TrimSpace(cfg.Secret)
	}

	body, code, err := doJSONRequest(ctx, strings.ToUpper(coalesceStr(cfg.Method, "GET")), url, headers)
	if err != nil {
		return FetchResult{}, err
	}
	if code < 200 || code >= 300 {
		return FetchResult{}, fmt.Errorf("自定义接口返回 HTTP %d: %s", code, truncateErrBody(body))
	}

	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return FetchResult{}, fmt.Errorf("响应不是合法 JSON: %w", err)
	}

	scale := cfg.Scale
	if scale == 0 {
		scale = 1
	}

	plan := cfg.PlanLabel
	if cfg.PlanPath != "" {
		if v, ok := extractDotPath(root, cfg.PlanPath); ok {
			plan = fmt.Sprintf("%v", v)
		}
	}

	if cfg.Kind == model.KindSubscription {
		var usedPct float64
		hasPct := false
		usedDisplay := ""

		if cfg.UsedPercentPath != "" {
			if raw, ok := extractDotPath(root, cfg.UsedPercentPath); ok {
				if f, ok := parseFlexibleFloat(raw); ok {
					usedPct = f * scale
					hasPct = true
					usedDisplay = fmt.Sprintf("已用 %.0f%%", usedPct)
				}
			}
		}
		if !hasPct && cfg.UsedPath != "" && cfg.LimitPath != "" {
			uRaw, ok1 := extractDotPath(root, cfg.UsedPath)
			lRaw, ok2 := extractDotPath(root, cfg.LimitPath)
			uVal, ok3 := parseFlexibleFloat(uRaw)
			lVal, ok4 := parseFlexibleFloat(lRaw)
			if ok1 && ok2 && ok3 && ok4 && lVal > 0 {
				uScaled := uVal * scale
				lScaled := lVal * scale
				usedPct = (uScaled / lScaled) * 100.0
				hasPct = true
				usedDisplay = fmt.Sprintf("%.1f / %.1f", uScaled, lScaled)
			}
		}
		if !hasPct {
			return FetchResult{}, errors.New("未能在 JSON 中提取到订阅百分比，请检查 usedPercentPath 或 usedPath/limitPath")
		}

		var resetAt *time.Time
		if cfg.ResetAtPath != "" {
			if rRaw, ok := extractDotPath(root, cfg.ResetAtPath); ok {
				resetAt = parseFlexibleTime(rRaw)
			}
		}
		win := makeWindow("custom", "配额使用", usedPct, resetAt, 0, usedDisplay)
		windows := []model.QuotaWindow{win}
		warn := 75.0
		if cfg.WarnPercent != nil && *cfg.WarnPercent > 0 {
			warn = *cfg.WarnPercent
		}
		st, reason, primary := evaluateWindowsStatus(windows, warn)
		return FetchResult{
			PlanLabel:     coalesceStr(plan, "Custom"),
			PrimaryMetric: primary,
			Status:        st,
			StatusReason:  reason,
			Windows:       windows,
		}, nil
	}

	// Balance mode
	var avail float64
	hasAvail := false
	if cfg.BalancePath != "" {
		if bRaw, ok := extractDotPath(root, cfg.BalancePath); ok {
			if f, ok := parseFlexibleFloat(bRaw); ok {
				avail = round2(f * scale)
				hasAvail = true
			}
		}
	}
	var usedPtr, limitPtr *float64
	if cfg.UsedPath != "" {
		if uRaw, ok := extractDotPath(root, cfg.UsedPath); ok {
			if f, ok := parseFlexibleFloat(uRaw); ok {
				v := round2(f * scale)
				usedPtr = &v
			}
		}
	}
	if cfg.LimitPath != "" {
		if lRaw, ok := extractDotPath(root, cfg.LimitPath); ok {
			if f, ok := parseFlexibleFloat(lRaw); ok {
				v := round2(f * scale)
				limitPtr = &v
			}
		}
	}
	if !hasAvail && usedPtr != nil && limitPtr != nil {
		avail = round2(*limitPtr - *usedPtr)
		hasAvail = true
	}
	if !hasAvail {
		return FetchResult{}, errors.New("未能在 JSON 中提取到余额，请检查 balancePath 配置")
	}

	cur := strings.ToUpper(coalesceStr(cfg.Currency, "USD"))
	sym := model.CurrencySymbol(cur)
	var details []model.KVDetail
	if limitPtr != nil {
		details = append(details, model.KVDetail{Label: "总额度", Value: fmt.Sprintf("%s%.2f", sym, *limitPtr)})
	}
	if usedPtr != nil {
		details = append(details, model.KVDetail{Label: "已消耗", Value: fmt.Sprintf("%s%.2f", sym, *usedPtr)})
	}
	bal := &model.BalanceInfo{
		Currency:     cur,
		Symbol:       sym,
		Available:    avail,
		Used:         usedPtr,
		Limit:        limitPtr,
		LowThreshold: cfg.LowThreshold,
		Details:      details,
	}
	st, reason, primary := evaluateBalanceStatus(bal)
	return FetchResult{
		PlanLabel:     coalesceStr(plan, "HTTP"),
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Balance:       bal,
	}, nil
}

func extractDotPath(root any, path string) (any, bool) {
	cur := root
	for _, seg := range strings.Split(strings.TrimSpace(path), ".") {
		if seg == "" {
			continue
		}
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, cur != nil
}

// ============================================================================
// 9. Manual / Cycle Subscription & Stored Balance (Cursor / Copilot / 任意会员)
// ============================================================================

type manualProvider struct{}

func (p *manualProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "manual",
		Name:                  "独立周期订阅 / 手动账本",
		DefaultBadge:          "SUB",
		DefaultKind:           model.KindSubscription,
		SupportsKindSwitch:    true,
		DefaultRefreshSeconds: 3600,
		Description:           "适用于 Cursor Pro、GitHub Copilot、Perplexity、会员套餐或无公开 API 的服务：支持自动计算账单重置倒计时、月度额度进度或手动储值余额。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "planLabel",
				Label:       "套餐名称（可选）",
				Type:        "text",
				Placeholder: "如 Pro $20/月、包年会员",
			},
			{
				Key:     "manualCycle",
				Label:   "重置 / 续费周期（订阅模式）",
				Type:    "select",
				Default: "monthly",
				Options: []model.Option{
					{Value: "monthly", Label: "按月重置"},
					{Value: "weekly", Label: "按周重置"},
					{Value: "yearly", Label: "按年续费"},
					{Value: "none", Label: "不设固定周期"},
				},
			},
			{
				Key:         "billingDay",
				Label:       "每月重置/账单日（1-31，按月模式）",
				Type:        "number",
				Default:     "1",
				Placeholder: "例如每月 15 号填 15",
			},
			{
				Key:         "nextResetDate",
				Label:       "指定下次重置/到期日（可选，YYYY-MM-DD）",
				Type:        "text",
				Placeholder: "2026-12-01（优先于账单日）",
			},
			{
				Key:         "quotaUsed",
				Label:       "当前已用额度（可选，订阅模式）",
				Type:        "number",
				Placeholder: "如 180",
			},
			{
				Key:         "quotaTotal",
				Label:       "周期总额度（可选，订阅模式）",
				Type:        "number",
				Placeholder: "如 500",
			},
			{
				Key:         "quotaUnit",
				Label:       "额度单位（可选）",
				Type:        "text",
				Placeholder: "次 / Credits / GB",
			},
			{
				Key:         "manualBalance",
				Label:       "当前可用余额（余额模式）",
				Type:        "number",
				Placeholder: "100.00",
			},
			{
				Key:     "currency",
				Label:   "币种",
				Type:    "select",
				Default: "USD",
				Options: []model.Option{
					{Value: "USD", Label: "美元 (USD $)"},
					{Value: "CNY", Label: "人民币 (CNY ¥)"},
				},
			},
			{
				Key:         "lowThreshold",
				Label:       "低余额预警阈值（余额模式可选）",
				Type:        "number",
				Placeholder: "20",
			},
		},
	}
}

func (p *manualProvider) Fetch(_ context.Context, cfg model.EntryConfig) (FetchResult, error) {
	now := time.Now()
	if cfg.Kind == model.KindBalance {
		val := 0.0
		if cfg.ManualBalance != nil {
			val = round2(*cfg.ManualBalance)
		}
		cur := strings.ToUpper(coalesceStr(cfg.Currency, "CNY"))
		sym := model.CurrencySymbol(cur)
		var details []model.KVDetail
		if cfg.NextResetDate != "" {
			details = append(details, model.KVDetail{Label: "到期/备注日", Value: cfg.NextResetDate})
		}
		bal := &model.BalanceInfo{
			Currency:     cur,
			Symbol:       sym,
			Available:    val,
			LowThreshold: cfg.LowThreshold,
			Details:      details,
		}
		st, reason, primary := evaluateBalanceStatus(bal)
		return FetchResult{
			PlanLabel:     coalesceStr(cfg.PlanLabel, "手动储值"),
			PrimaryMetric: primary,
			Status:        st,
			StatusReason:  reason,
			Balance:       bal,
		}, nil
	}

	// Subscription mode
	nextReset, cycleSec := computeNextCycleWindow(now, cfg.ManualCycle, cfg.BillingDay, cfg.NextResetDate)
	var windows []model.QuotaWindow

	if cfg.QuotaTotal != nil && *cfg.QuotaTotal > 0 {
		used := 0.0
		if cfg.QuotaUsed != nil {
			used = *cfg.QuotaUsed
		}
		pct := (used / *cfg.QuotaTotal) * 100.0
		unit := strings.TrimSpace(cfg.QuotaUnit)
		disp := fmt.Sprintf("%s / %s", trimFloat(used), trimFloat(*cfg.QuotaTotal))
		if unit != "" {
			disp += " " + unit
		}
		windows = append(windows, makeWindow("quota", "套餐额度", pct, nextReset, cycleSec, disp))
	}

	if nextReset != nil && cycleSec > 0 {
		remSec := nextReset.Sub(now).Seconds()
		elapsedPct := clampPercent(((float64(cycleSec) - remSec) / float64(cycleSec)) * 100.0)
		daysLeft := int(math.Ceil(remSec / 86400.0))
		if daysLeft < 0 {
			daysLeft = 0
		}
		windows = append(windows, model.QuotaWindow{
			Key:              "cycle",
			Label:            "计费周期",
			UsedPercent:      math.Round(elapsedPct*10) / 10,
			RemainingPercent: math.Round((100-elapsedPct)*10) / 10,
			UsedDisplay:      fmt.Sprintf("剩 %d 天重置", daysLeft),
			ResetsAt:         nextReset,
			WindowSeconds:    cycleSec,
		})
	}

	if len(windows) == 0 {
		return FetchResult{
			PlanLabel:     coalesceStr(cfg.PlanLabel, "订阅中"),
			PrimaryMetric: coalesceStr(cfg.PlanLabel, "生效中"),
			Status:        model.StatusOK,
		}, nil
	}

	// Only evaluate warning threshold against actual quota window if present; pure time cycle shouldn't turn red just because billing day is tomorrow!
	if cfg.QuotaTotal != nil && *cfg.QuotaTotal > 0 {
		warn := 75.0
		if cfg.WarnPercent != nil && *cfg.WarnPercent > 0 {
			warn = *cfg.WarnPercent
		}
		st, reason, primary := evaluateWindowsStatus(windows[:1], warn)
		return FetchResult{
			PlanLabel:     coalesceStr(cfg.PlanLabel, "订阅"),
			PrimaryMetric: primary,
			Status:        st,
			StatusReason:  reason,
			Windows:       windows,
		}, nil
	}

	daysLeft := 0
	if nextReset != nil {
		daysLeft = int(math.Ceil(nextReset.Sub(now).Hours() / 24.0))
		if daysLeft < 0 {
			daysLeft = 0
		}
	}
	return FetchResult{
		PlanLabel:     coalesceStr(cfg.PlanLabel, "周期订阅"),
		PrimaryMetric: fmt.Sprintf("%d天后续期", daysLeft),
		Status:        model.StatusOK,
		Windows:       windows,
	}, nil
}

func computeNextCycleWindow(now time.Time, cycle string, billingDay int, explicitDate string) (*time.Time, int64) {
	loc := now.Location()
	if s := strings.TrimSpace(explicitDate); s != "" {
		if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
			end := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
			for !end.After(now) {
				switch cycle {
				case "yearly":
					end = end.AddDate(1, 0, 0)
				case "weekly":
					end = end.AddDate(0, 0, 7)
				default:
					end = end.AddDate(0, 1, 0)
				}
			}
			u := end.UTC()
			sec := int64(30 * 86400)
			if cycle == "yearly" {
				sec = 365 * 86400
			} else if cycle == "weekly" {
				sec = 7 * 86400
			}
			return &u, sec
		}
	}

	switch cycle {
	case "none":
		return nil, 0
	case "weekly":
		// Next Monday 00:00 local time
		daysUntilMon := (8 - int(now.Weekday())) % 7
		if daysUntilMon == 0 {
			daysUntilMon = 7
		}
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, daysUntilMon)
		u := next.UTC()
		return &u, 7 * 86400
	case "yearly":
		day := clampDay(now.Year(), now.Month(), billingDay)
		next := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, loc)
		if !next.After(now) {
			next = time.Date(now.Year()+1, now.Month(), clampDay(now.Year()+1, now.Month(), billingDay), 0, 0, 0, 0, loc)
		}
		u := next.UTC()
		return &u, 365 * 86400
	default: // monthly
		if billingDay < 1 || billingDay > 31 {
			billingDay = 1
		}
		thisMonthDay := clampDay(now.Year(), now.Month(), billingDay)
		candidate := time.Date(now.Year(), now.Month(), thisMonthDay, 0, 0, 0, 0, loc)
		var prev, next time.Time
		if candidate.After(now) {
			next = candidate
			pm := now.AddDate(0, -1, 0)
			prev = time.Date(pm.Year(), pm.Month(), clampDay(pm.Year(), pm.Month(), billingDay), 0, 0, 0, 0, loc)
		} else {
			prev = candidate
			nm := now.AddDate(0, 1, 0)
			next = time.Date(nm.Year(), nm.Month(), clampDay(nm.Year(), nm.Month(), billingDay), 0, 0, 0, 0, loc)
		}
		u := next.UTC()
		return &u, int64(next.Sub(prev).Seconds())
	}
}

func clampDay(year int, month time.Month, day int) int {
	if day < 1 {
		return 1
	}
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		return lastDay
	}
	return day
}

func trimFloat(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}
