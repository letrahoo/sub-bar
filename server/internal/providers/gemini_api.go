package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
)

const (
	defaultGeminiAPIBase    = "https://generativelanguage.googleapis.com"
	defaultGeminiProbeModel = "gemini-2.5-flash"
	geminiProbeModeModels   = "models"
	geminiProbeModeGenerate = "generate"
)

// ============================================================================
// Google Gemini API Key Status Provider (Google AI Studio)
// ============================================================================

type geminiAPIProvider struct{}

func (p *geminiAPIProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "gemini_api",
		Name:                  "Google Gemini API",
		DefaultBadge:          "GEM",
		DefaultKind:           model.KindBalance,
		DefaultRefreshSeconds: 600,
		DefaultDashboardURL:   "https://aistudio.google.com/billing",
		Description:           "使用 Google AI Studio API Key 监控密钥可用性与可用模型数，支持可选真实调用检测 402 余额不足与 429 限流状态（Google 官方未开放 API Key 余额查询接口，点击官网面板可直达 AI Studio 账单页）。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "secret",
				Label:       "Google Gemini API Key",
				Type:        "secret",
				Required:    true,
				Placeholder: "AIza...",
				Help:        "在 Google AI Studio (aistudio.google.com/apikey) 创建，仅保存在本机。",
			},
			{
				Key:     "probeMode",
				Label:   "检测方式",
				Type:    "select",
				Default: geminiProbeModeModels,
				Options: []model.Option{
					{Value: geminiProbeModeModels, Label: "仅校验密钥与可用模型（零费用，推荐）"},
					{Value: geminiProbeModeGenerate, Label: "真实调用检测（可识别 402 余额不足 / 429 限流）"},
				},
				Help: "「仅校验密钥」免费且不消耗调用次数；「真实调用检测」每次刷新发一次极短测试请求（约十余 Token），用于发现欠费或限流。",
			},
			{
				Key:         "probeModel",
				Label:       "检测模型（可选，真实调用模式生效）",
				Type:        "text",
				Placeholder: "留空自动选用 Flash 模型（如 gemini-2.5-flash）",
			},
			{
				Key:         "baseUrl",
				Label:       "自定义接口地址（可选）",
				Type:        "text",
				Placeholder: "默认 https://generativelanguage.googleapis.com",
				Help:        "如通过代理或中转访问可填写根地址，留空直连 Google 官方。",
			},
		},
	}
}

func (p *geminiAPIProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	key := strings.TrimSpace(cfg.Secret)
	if key == "" {
		return FetchResult{}, errors.New("未填写 Google Gemini API Key")
	}

	apiBase, err := resolveGeminiAPIBase(cfg.BaseURL)
	if err != nil {
		return FetchResult{}, err
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.ProbeMode))
	if mode != geminiProbeModeGenerate {
		mode = geminiProbeModeModels
	}

	headers := map[string]string{
		"x-goog-api-key": key,
	}
	plan := coalesceStr(cfg.PlanLabel, "API Key")

	// Step 1: Zero-cost key validation & model listing (GET /v1beta/models?pageSize=100)
	modelsStart := time.Now()
	modelsBody, modelsCode, err := doJSONRequest(ctx, http.MethodGet, apiBase+"/models?pageSize=100", headers)
	modelsLatency := time.Since(modelsStart)
	if err != nil {
		return FetchResult{}, err
	}

	if modelsCode < 200 || modelsCode >= 300 {
		if res, handled := classifyGeminiStatusError(modelsCode, modelsBody, plan, 0, "", mode); handled {
			return res, nil
		}
		return FetchResult{}, formatGeminiHardError(modelsCode, modelsBody, "")
	}

	modelCount, featuredModel, autoProbeModel, err := parseGeminiModelsList(modelsBody)
	if err != nil {
		return FetchResult{}, err
	}

	if mode == geminiProbeModeModels {
		details := []model.KVDetail{
			{Label: "可用模型", Value: fmt.Sprintf("%d 个", modelCount)},
			{Label: "检测方式", Value: "密钥校验"},
			{Label: "响应延迟", Value: formatLatencyMS(modelsLatency)},
		}
		if featuredModel != "" {
			details = append(details, model.KVDetail{Label: "代表模型", Value: featuredModel})
		}
		return FetchResult{
			PlanLabel:     plan,
			PrimaryMetric: "可用",
			Status:        model.StatusOK,
			Details:       details,
		}, nil
	}

	// Step 2: Optional minimal generateContent probe to detect HTTP 402 (billing/prepay depleted) & HTTP 429 (quota/rate limit).
	targetModel, err := normalizeGeminiModelID(cfg.ProbeModel, autoProbeModel)
	if err != nil {
		return FetchResult{}, err
	}

	probePayload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{
					{"text": "ping"},
				},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 16,
			"temperature":     0,
		},
	}

	probeURL := fmt.Sprintf("%s/models/%s:generateContent", apiBase, url.PathEscape(targetModel))
	probeStart := time.Now()
	probeBody, probeCode, err := doJSONBodyRequest(ctx, http.MethodPost, probeURL, headers, probePayload)
	probeLatency := time.Since(probeStart)
	if err != nil {
		return FetchResult{}, err
	}

	if probeCode < 200 || probeCode >= 300 {
		if res, handled := classifyGeminiStatusError(probeCode, probeBody, plan, modelCount, targetModel, mode); handled {
			return res, nil
		}
		return FetchResult{}, formatGeminiHardError(probeCode, probeBody, targetModel)
	}

	return FetchResult{
		PlanLabel:     plan,
		PrimaryMetric: "可用",
		Status:        model.StatusOK,
		Details: []model.KVDetail{
			{Label: "可用模型", Value: fmt.Sprintf("%d 个", modelCount)},
			{Label: "检测模型", Value: targetModel},
			{Label: "响应延迟", Value: formatLatencyMS(probeLatency)},
			{Label: "检测方式", Value: "真实调用"},
		},
	}, nil
}

func resolveGeminiAPIBase(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = defaultGeminiAPIBase
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || strings.TrimSpace(u.Host) == "" {
		return "", errors.New("自定义接口地址必须是有效的 http(s) URL")
	}
	base = strings.TrimRight(base, "/")
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, "/v1beta") || strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v1alpha") {
		return base, nil
	}
	return base + "/v1beta", nil
}

func normalizeGeminiModelID(configured, fallback string) (string, error) {
	m := strings.TrimSpace(configured)
	if m == "" {
		m = strings.TrimSpace(fallback)
	}
	if m == "" {
		m = defaultGeminiProbeModel
	}
	m = strings.TrimPrefix(m, "models/")
	m = strings.TrimSpace(m)
	if m == "" || strings.ContainsAny(m, "/?#\\") || strings.Contains(m, "..") {
		return "", fmt.Errorf("检测模型名称格式无效: %q", configured)
	}
	return m, nil
}

func parseGeminiModelsList(raw []byte) (count int, featured string, probeModel string, err error) {
	var doc struct {
		Models []struct {
			Name                       string   `json:"name"`
			BaseModelID                string   `json:"baseModelId"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, "", "", fmt.Errorf("解析 Google Gemini 模型列表失败: %w", err)
	}
	if len(doc.Models) == 0 {
		return 0, "", "", errors.New("Google Gemini 未返回可用模型列表，请检查 API Key 或接口地址")
	}

	var genGemini []string
	for _, m := range doc.Models {
		id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
		if id == "" {
			id = strings.TrimSpace(m.BaseModelID)
		}
		if id == "" {
			continue
		}
		supportsGen := len(m.SupportedGenerationMethods) == 0
		for _, meth := range m.SupportedGenerationMethods {
			if strings.EqualFold(meth, "generateContent") {
				supportsGen = true
				break
			}
		}
		if !supportsGen {
			continue
		}
		lower := strings.ToLower(id)
		if strings.Contains(lower, "gemini") {
			genGemini = append(genGemini, id)
		}
	}

	count = len(genGemini)
	if count == 0 {
		count = len(doc.Models)
	}

	probeModel = pickGeminiProbeModel(genGemini)
	featured = pickGeminiFeaturedModel(genGemini)
	if featured == "" {
		featured = probeModel
	}
	return count, featured, probeModel, nil
}

func pickGeminiProbeModel(ids []string) string {
	preferred := []string{
		"gemini-2.5-flash-lite",
		"gemini-2.5-flash",
		"gemini-2.0-flash-lite",
		"gemini-2.0-flash",
	}
	for _, want := range preferred {
		for _, id := range ids {
			if strings.EqualFold(id, want) {
				return id
			}
		}
	}
	for _, id := range ids {
		lower := strings.ToLower(id)
		if strings.Contains(lower, "flash") &&
			!strings.Contains(lower, "image") &&
			!strings.Contains(lower, "audio") &&
			!strings.Contains(lower, "tts") &&
			!strings.Contains(lower, "live") {
			return id
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return defaultGeminiProbeModel
}

func pickGeminiFeaturedModel(ids []string) string {
	for _, id := range ids {
		lower := strings.ToLower(id)
		if strings.Contains(lower, "pro") &&
			!strings.Contains(lower, "image") &&
			!strings.Contains(lower, "tts") &&
			!strings.Contains(lower, "vision") {
			return id
		}
	}
	return pickGeminiProbeModel(ids)
}

type geminiRPCError struct {
	Code        int
	Status      string
	Message     string
	Reason      string
	QuotaMetric string
	QuotaID     string
	RetryDelay  string
}

func parseGeminiRPCError(raw []byte) geminiRPCError {
	var doc struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type       string `json:"@type"`
				Reason     string `json:"reason"`
				RetryDelay string `json:"retryDelay"`
				Violations []struct {
					QuotaMetric string `json:"quotaMetric"`
					QuotaID     string `json:"quotaId"`
				} `json:"violations"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Error == nil {
		return geminiRPCError{}
	}
	out := geminiRPCError{
		Code:    doc.Error.Code,
		Status:  strings.TrimSpace(doc.Error.Status),
		Message: strings.TrimSpace(doc.Error.Message),
	}
	for _, d := range doc.Error.Details {
		if d.Reason != "" && out.Reason == "" {
			out.Reason = strings.TrimSpace(d.Reason)
		}
		if d.RetryDelay != "" && out.RetryDelay == "" {
			out.RetryDelay = strings.TrimSpace(d.RetryDelay)
		}
		for _, v := range d.Violations {
			if out.QuotaMetric == "" && strings.TrimSpace(v.QuotaMetric) != "" {
				out.QuotaMetric = strings.TrimSpace(v.QuotaMetric)
			}
			if out.QuotaID == "" && strings.TrimSpace(v.QuotaID) != "" {
				out.QuotaID = strings.TrimSpace(v.QuotaID)
			}
		}
	}
	return out
}

func classifyGeminiStatusError(code int, body []byte, plan string, modelCount int, targetModel string, mode string) (FetchResult, bool) {
	rpcErr := parseGeminiRPCError(body)
	msgLower := strings.ToLower(rpcErr.Message)

	isBillingDepleted := code == http.StatusPaymentRequired ||
		strings.EqualFold(rpcErr.Reason, "BILLING_DISABLED") ||
		(code == http.StatusForbidden && (strings.Contains(msgLower, "billing") || strings.Contains(msgLower, "prepaid") || strings.Contains(msgLower, "spend cap")))

	if isBillingDepleted {
		details := []model.KVDetail{
			{Label: "账单状态", Value: "预付余额或结算受限"},
		}
		if targetModel != "" {
			details = append(details, model.KVDetail{Label: "检测模型", Value: targetModel})
		}
		if modelCount > 0 {
			details = append(details, model.KVDetail{Label: "可用模型", Value: fmt.Sprintf("%d 个", modelCount)})
		}
		if hint := compactGeminiMessage(rpcErr.Message); hint != "" {
			details = append(details, model.KVDetail{Label: "上游提示", Value: hint})
		}
		reason := fmt.Sprintf("余额不足 (HTTP %d)", code)
		if code == http.StatusForbidden {
			reason = "账单未启用或受限"
		}
		return FetchResult{
			PlanLabel:     plan,
			PrimaryMetric: "余额不足",
			Status:        model.StatusCritical,
			StatusReason:  reason,
			Details:       details,
		}, true
	}

	isRateLimited := code == http.StatusTooManyRequests || strings.EqualFold(rpcErr.Status, "RESOURCE_EXHAUSTED")
	if isRateLimited {
		zeroLimit := strings.Contains(msgLower, "limit: 0") || strings.Contains(msgLower, "limit\": 0")
		statusReason := "触发限流 (429)"
		limitSummary := "请求或 Token 达到上限"
		if zeroLimit {
			statusReason = "配额为 0 (429)"
			limitSummary = "模型可用配额为 0"
		}

		details := []model.KVDetail{
			{Label: "限流状态", Value: limitSummary},
		}
		if targetModel != "" {
			details = append(details, model.KVDetail{Label: "检测模型", Value: targetModel})
		}
		if metric := shortenGeminiQuotaMetric(rpcErr.QuotaMetric, rpcErr.QuotaID); metric != "" {
			details = append(details, model.KVDetail{Label: "配额维度", Value: metric})
		}
		if rpcErr.RetryDelay != "" {
			details = append(details, model.KVDetail{Label: "重试间隔", Value: rpcErr.RetryDelay})
		}
		if modelCount > 0 {
			details = append(details, model.KVDetail{Label: "可用模型", Value: fmt.Sprintf("%d 个", modelCount)})
		}
		if mode == geminiProbeModeModels && targetModel == "" {
			details = append(details, model.KVDetail{Label: "检测方式", Value: "密钥校验"})
		}

		return FetchResult{
			PlanLabel:     plan,
			PrimaryMetric: "限流",
			Status:        model.StatusWarning,
			StatusReason:  statusReason,
			Details:       details,
		}, true
	}

	return FetchResult{}, false
}

func formatGeminiHardError(code int, body []byte, targetModel string) error {
	rpcErr := parseGeminiRPCError(body)
	detail := coalesceStr(rpcErr.Message, truncateErrBody(body))
	reasonUpper := strings.ToUpper(rpcErr.Reason)
	msgLower := strings.ToLower(rpcErr.Message)

	switch {
	case reasonUpper == "API_KEY_INVALID" || reasonUpper == "API_KEY_EXPIRED" ||
		strings.Contains(msgLower, "api key not valid") || strings.Contains(msgLower, "api key expired") ||
		code == http.StatusUnauthorized:
		return fmt.Errorf("Google Gemini API Key 无效或已过期 (HTTP %d)", code)
	case reasonUpper == "SERVICE_DISABLED":
		return fmt.Errorf("当前项目未启用 Generative Language API (HTTP %d)", code)
	case code == http.StatusForbidden:
		if detail != "" {
			return fmt.Errorf("Google Gemini API Key 权限不足 (HTTP 403): %s", compactGeminiMessage(detail))
		}
		return errors.New("Google Gemini API Key 权限不足 (HTTP 403)")
	case code == http.StatusNotFound && targetModel != "":
		return fmt.Errorf("检测模型 %q 不存在或当前密钥无权访问 (HTTP 404)", targetModel)
	default:
		return fmt.Errorf("Google Gemini 返回 HTTP %d: %s", code, compactGeminiMessage(detail))
	}
}

func shortenGeminiQuotaMetric(metric, quotaID string) string {
	s := strings.TrimSpace(metric)
	if idx := strings.LastIndex(s, "/"); idx >= 0 && idx+1 < len(s) {
		s = s[idx+1:]
	}
	if s == "" {
		s = strings.TrimSpace(quotaID)
	}
	if len(s) > 42 {
		return s[:42] + "…"
	}
	return s
}

func compactGeminiMessage(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

func formatLatencyMS(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return fmt.Sprintf("%dms", ms)
}
