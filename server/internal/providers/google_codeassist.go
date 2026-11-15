package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"xin.letra/sub-cost-view/server/internal/model"
)

const (
	googleAICodeProd     = "https://aicode.googleapis.com"
	googleCloudCodeProd  = "https://cloudcode-pa.googleapis.com"
	googleCloudCodeDaily = "https://daily-cloudcode-pa.googleapis.com"
)

// agyCLIUsageRunner allows hermetic unit tests to stub `agy -p /usage --output-format json`.
var agyCLIUsageRunner func(ctx context.Context) ([]byte, error)

// ============================================================================
// 1. Antigravity (agy) Quota Provider
// ============================================================================

type antigravityProvider struct{}

func (p *antigravityProvider) Descriptor() model.ProviderDescriptor {
	return model.ProviderDescriptor{
		ID:                    "antigravity_oauth",
		Name:                  "Antigravity (agy)",
		DefaultBadge:          "AGY",
		DefaultKind:           model.KindSubscription,
		SupportsOAuthFlow:     true,
		OAuthButtonLabel:      "打开 Google 授权链接（一键接入 Antigravity）",
		DefaultRefreshSeconds: 300,
		DefaultDashboardURL:   "https://antigravity.google",
		Description:           "通过浏览器点击 Google 授权链接一键接入 Antigravity，完整监控 Gemini 与 Claude / GPT 的 5 小时限额及 7 天周限额重置倒计时，支持后台无感续期与多账号。",
		Fields: []model.ProviderFieldSchema{
			{
				Key:         "planLabel",
				Label:       "套餐标记（可选，留空自动识别）",
				Type:        "text",
				Placeholder: "如 AI Pro / Ultra",
			},
		},
	}
}

func (p *antigravityProvider) Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	return fetchGoogleCodeAssist(ctx, cfg)
}

// ============================================================================
// Google Cloud Code Assist OAuth & Quota Engine (Antigravity)
// ============================================================================

func fetchGoogleCodeAssist(ctx context.Context, cfg model.EntryConfig) (FetchResult, error) {
	providerID := "antigravity_oauth"
	userAgent := "antigravity/2.0.0 darwin/arm64"
	ideType := "ANTIGRAVITY"

	var tokenUpd *TokenUpdate
	accessToken := strings.TrimSpace(cfg.Secret)
	refreshedAlready := false

	if tokenNeedsProactiveRefresh(cfg) {
		upd, err := RefreshOAuthToken(ctx, providerID, cfg.RefreshToken)
		if err != nil {
			if accessToken == "" {
				return FetchResult{}, err
			}
		} else {
			accessToken = upd.AccessToken
			tokenUpd = &upd
			refreshedAlready = true
		}
	}

	if accessToken == "" {
		return FetchResult{}, errors.New("尚未完成 Google 登录授权，请点击编辑中的「打开 Google 授权链接」完成接入")
	}

	runAttempt := func(tok string) (string, string, int64, []summaryWindowItem, []rawModelQuotaItem, int, error) {
		// Antigravity (agy CLI & Desktop 2.0) tracks live model quotas on daily-cloudcode-pa + aicode.googleapis.com;
		// cloudcode-pa is kept only as a fallback if daily is unreachable.
		bases := []string{googleCloudCodeDaily, googleCloudCodeProd}
		customBase := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
		if customBase != "" {
			bases = []string{customBase}
		}
		hdrs := map[string]string{
			"Authorization": "Bearer " + tok,
			"User-Agent":    userAgent,
		}

		projectID := strings.TrimSpace(cfg.ProjectID)
		tierLabel := ""
		var aiCredits int64
		lastStatus := 0
		activeBases := bases

		// Step 1: loadCodeAssist for projectId, subscription tier & Google One AI credits
		loadBody := map[string]any{
			"metadata": map[string]string{
				"ideType":    ideType,
				"platform":   "PLATFORM_UNSPECIFIED",
				"pluginType": "GEMINI",
			},
		}
		for i, base := range bases {
			raw, code, err := doJSONBodyRequest(ctx, http.MethodPost, base+"/v1internal:loadCodeAssist", hdrs, loadBody)
			lastStatus = code
			if code == http.StatusUnauthorized {
				return "", "", 0, nil, nil, code, errors.New("Google OAuth 令牌已过期")
			}
			if err == nil && code >= 200 && code < 300 {
				pid, tier, credits := parseLoadCodeAssist(raw)
				if pid != "" {
					projectID = pid
				}
				if tier != "" {
					tierLabel = tier
				}
				if credits > 0 {
					aiCredits = credits
				}
				// Pin the winning host first so quota queries hit the exact same backend pool.
				activeBases = append([]string{base}, bases[i+1:]...)
				break
			}
		}

		reqPayload := map[string]any{}
		if projectID != "" {
			reqPayload["project"] = projectID
		}

		var summaryItems []summaryWindowItem
		var items []rawModelQuotaItem
		var lastErr error
		aicodeScopeMissing := false

		// Step 2A: Query AICode FetchQuotaStatus (`GET https://aicode.googleapis.com/v1alpha:fetchQuotaStatus`).
		// `agy` 1.5+ routes consumer/pro/ultra accounts to AICode, which returns BOTH `Gemini Models` (`gemini-weekly` + `gemini-5h`)
		// AND `Claude and GPT models` (`3p-weekly` + `3p-5h`), whereas CCPA `retrieveUserQuotaSummary` only returns 3P buckets.
		if customBase == "" {
			raw, code, err := doJSONRequest(ctx, http.MethodGet, googleAICodeProd+"/v1alpha:fetchQuotaStatus", hdrs)
			if code == http.StatusUnauthorized {
				return projectID, tierLabel, aiCredits, nil, nil, code, errors.New("Google OAuth 令牌已过期")
			}
			if err == nil && code >= 200 && code < 300 {
				aSummary, aModels, aCredits := parseAICodeFetchQuotaStatus(raw)
				if len(aSummary) > 0 {
					summaryItems = aSummary
				}
				if len(aModels) > 0 {
					items = aModels
				}
				if aCredits > 0 && aiCredits == 0 {
					aiCredits = aCredits
				}
			} else if code == http.StatusForbidden && strings.Contains(string(raw), "ACCESS_TOKEN_SCOPE_INSUFFICIENT") {
				aicodeScopeMissing = true
			}
		}

		// Step 2B: Fallback to CCPA RetrieveUserQuotaSummary when AICode summary wasn't returned.
		if len(summaryItems) == 0 {
			for _, base := range activeBases {
				raw, code, err := doJSONBodyRequest(ctx, http.MethodPost, base+"/v1internal:retrieveUserQuotaSummary", hdrs, reqPayload)
				if code == http.StatusUnauthorized {
					return projectID, tierLabel, aiCredits, nil, nil, code, errors.New("Google OAuth 令牌已过期")
				}
				if err == nil && code >= 200 && code < 300 {
					parsed := parseRetrieveUserQuotaSummary(raw)
					if len(parsed) > 0 {
						summaryItems = parsed
					}
					break
				}
			}
		}

		// Step 3: Query per-model quota endpoints when summary does not already cover both Gemini and 3P families.
		tryFetchModels := func() bool {
			for _, base := range activeBases {
				raw, code, err := doJSONBodyRequest(ctx, http.MethodPost, base+"/v1internal:fetchAvailableModels", hdrs, reqPayload)
				lastStatus = code
				if code == http.StatusUnauthorized {
					lastErr = errors.New("Google OAuth 令牌已过期")
					return false
				}
				if err != nil {
					lastErr = err
					continue
				}
				if code >= 200 && code < 300 {
					parsed := parseFetchAvailableModels(raw)
					if len(parsed) > 0 {
						items = parsed
						return true
					}
				} else {
					lastErr = fmt.Errorf("上游返回 HTTP %d: %s", code, truncateErrBody(raw))
				}
			}
			return false
		}

		tryRetrieveQuota := func() bool {
			for _, base := range activeBases {
				raw, code, err := doJSONBodyRequest(ctx, http.MethodPost, base+"/v1internal:retrieveUserQuota", hdrs, reqPayload)
				lastStatus = code
				if code == http.StatusUnauthorized {
					lastErr = errors.New("Google OAuth 令牌已过期")
					return false
				}
				if err != nil {
					lastErr = err
					continue
				}
				if code >= 200 && code < 300 {
					parsed := parseRetrieveUserQuota(raw)
					if len(parsed) > 0 {
						items = parsed
						return true
					}
				} else {
					lastErr = fmt.Errorf("上游返回 HTTP %d: %s", code, truncateErrBody(raw))
				}
			}
			return false
		}

		if (!hasSummaryFamily(summaryItems, "gemini") || !hasSummaryFamily(summaryItems, "3p")) && len(items) == 0 {
			if !tryFetchModels() && lastStatus != http.StatusUnauthorized {
				_ = tryRetrieveQuota()
			}
		}

		// Step 4: If an older OAuth token lacks `auth/aicode` scope (missing `gemini_weekly`), seamlessly query local `agy -p /usage --output-format json`
		// when installed and logged into the same account (verified via matching quota reset timestamps).
		if aicodeScopeMissing && !hasSummaryWindowKey(summaryItems, "gemini_weekly") && customBase == "" {
			if cliSummary := tryLocalAgyUsageSummary(ctx, summaryItems, items, cfg.AccountEmail); len(cliSummary) > 0 {
				summaryItems = cliSummary
			}
		}

		if lastStatus == http.StatusUnauthorized {
			return projectID, tierLabel, aiCredits, nil, nil, lastStatus, lastErr
		}
		if len(summaryItems) == 0 && len(items) == 0 && lastErr != nil {
			return projectID, tierLabel, aiCredits, nil, nil, lastStatus, lastErr
		}
		return projectID, tierLabel, aiCredits, summaryItems, items, lastStatus, nil
	}

	projectID, tierLabel, aiCredits, summaryItems, items, status, err := runAttempt(accessToken)
	if status == http.StatusUnauthorized && !refreshedAlready && strings.TrimSpace(cfg.RefreshToken) != "" {
		upd, rErr := RefreshOAuthToken(ctx, providerID, cfg.RefreshToken)
		if rErr != nil {
			return FetchResult{}, rErr
		}
		tokenUpd = &upd
		projectID, tierLabel, aiCredits, summaryItems, items, _, err = runAttempt(upd.AccessToken)
	}
	if err != nil {
		return FetchResult{}, err
	}

	if projectID != "" && projectID != cfg.ProjectID {
		if tokenUpd == nil {
			tokenUpd = &TokenUpdate{ProjectID: projectID}
		} else {
			tokenUpd.ProjectID = projectID
		}
	}

	windows := mergeAntigravityWindows(summaryItems, items)
	warnPct := 75.0
	if cfg.WarnPercent != nil && *cfg.WarnPercent > 0 {
		warnPct = *cfg.WarnPercent
	}
	st, reason, primary := evaluateWindowsStatus(windows, warnPct)
	if len(windows) == 0 {
		primary = "已连接"
	}

	plan := strings.TrimSpace(cfg.PlanLabel)
	if plan == "" {
		plan = normalizeGoogleTier(tierLabel)
		if aiCredits > 0 {
			if plan != "" {
				plan = fmt.Sprintf("%s · %d Credits", plan, aiCredits)
			} else {
				plan = fmt.Sprintf("%d Credits", aiCredits)
			}
		}
	}
	accountEmail := cfg.AccountEmail
	if tokenUpd != nil && tokenUpd.AccountEmail != "" {
		accountEmail = tokenUpd.AccountEmail
	}

	return FetchResult{
		PlanLabel:     plan,
		AccountEmail:  accountEmail,
		PrimaryMetric: primary,
		Status:        st,
		StatusReason:  reason,
		Windows:       windows,
		TokenUpdate:   tokenUpd,
	}, nil
}

type rawModelQuotaItem struct {
	ModelID           string
	DisplayName       string
	RemainingFraction float64
	ResetTime         *time.Time
}

type summaryWindowItem struct {
	Family      string // "gemini" | "3p" | "other"
	Key         string
	Label       string
	Order       int
	UsedPercent float64
	WindowSec   int64
	ResetTime   *time.Time
}

func parseLoadCodeAssist(raw []byte) (projectID string, tierLabel string, aiCredits int64) {
	type creditItem struct {
		CreditType   string `json:"creditType"`
		CreditAmount any    `json:"creditAmount"`
	}
	type tierInfo struct {
		ID               string       `json:"id"`
		Name             string       `json:"name"`
		AvailableCredits []creditItem `json:"availableCredits"`
	}
	var doc struct {
		CloudAICompanionProject any       `json:"cloudaicompanionProject"`
		CurrentTier             *tierInfo `json:"currentTier"`
		PaidTier                *tierInfo `json:"paidTier"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", 0
	}
	switch v := doc.CloudAICompanionProject.(type) {
	case string:
		projectID = strings.TrimSpace(v)
	case map[string]any:
		if id, ok := v["id"].(string); ok {
			projectID = strings.TrimSpace(id)
		}
	}
	extractCredits := func(t *tierInfo) int64 {
		if t == nil {
			return 0
		}
		for _, c := range t.AvailableCredits {
			if f, ok := parseFlexibleFloat(c.CreditAmount); ok && f > 0 {
				return int64(math.Round(f))
			}
		}
		return 0
	}
	if doc.PaidTier != nil {
		tierLabel = coalesceStr(doc.PaidTier.Name, doc.PaidTier.ID)
		aiCredits = extractCredits(doc.PaidTier)
	}
	if tierLabel == "" && doc.CurrentTier != nil {
		tierLabel = coalesceStr(doc.CurrentTier.Name, doc.CurrentTier.ID)
	}
	if aiCredits == 0 && doc.CurrentTier != nil {
		aiCredits = extractCredits(doc.CurrentTier)
	}
	return projectID, tierLabel, aiCredits
}

type rawQuotaSummaryBucket struct {
	BucketID           string   `json:"bucketId"`
	ID                 string   `json:"id"`
	DisplayName        string   `json:"displayName"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Window             string   `json:"window"`
	RefreshWindowLabel string   `json:"refreshWindowLabel"`
	RemainingFraction  *float64 `json:"remainingFraction"`
	RemainingFracSnake *float64 `json:"remaining_fraction"`
	RemainingAmount    any      `json:"remainingAmount"`
	Disabled           bool     `json:"disabled"`
	ResetTime          string   `json:"resetTime"`
	ResetTimeSnake     string   `json:"reset_time"`
}

type rawQuotaSummaryGroup struct {
	DisplayName string                  `json:"displayName"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Buckets     []rawQuotaSummaryBucket `json:"buckets"`
}

func convertQuotaSummaryBucket(groupName string, b rawQuotaSummaryBucket) (summaryWindowItem, bool) {
	bucketID := coalesceStr(b.BucketID, b.ID)
	displayName := coalesceStr(b.DisplayName, b.Name)
	windowRaw := coalesceStr(b.Window, b.RefreshWindowLabel)
	resetStr := coalesceStr(b.ResetTime, b.ResetTimeSnake)
	remPtr := b.RemainingFraction
	if remPtr == nil {
		remPtr = b.RemainingFracSnake
	}

	if remPtr == nil && b.RemainingAmount == nil && resetStr == "" && !b.Disabled {
		return summaryWindowItem{}, false
	}
	rem := 1.0
	if b.Disabled {
		rem = 0.0
	} else if remPtr != nil {
		rem = *remPtr
	} else if resetStr != "" || strings.Contains(strings.ToLower(b.Description), "exhausted") {
		rem = 0.0
	}
	if rem < 0 {
		rem = 0
	}
	if rem > 1 {
		rem = 1
	}

	combined := strings.ToLower(groupName + " " + bucketID + " " + displayName)
	family := "other"
	baseLabel := strings.TrimSpace(groupName)
	familyBaseOrder := 20
	switch {
	case strings.Contains(combined, "gemini") || strings.HasPrefix(strings.ToLower(bucketID), "1p"):
		family = "gemini"
		baseLabel = "Gemini"
		familyBaseOrder = 1
	case strings.Contains(combined, "claude") || strings.Contains(combined, "gpt") || strings.HasPrefix(strings.ToLower(bucketID), "3p"):
		family = "3p"
		baseLabel = "Claude / GPT"
		familyBaseOrder = 10
	default:
		if baseLabel == "" {
			baseLabel = coalesceStr(displayName, bucketID)
		}
	}

	winLower := strings.ToLower(strings.TrimSpace(windowRaw))
	var winSec int64 = 18000
	winSuffix := ""
	winOrder := 1
	switch {
	case winLower == "5h" || strings.Contains(strings.ToLower(bucketID), "5h") || strings.Contains(strings.ToLower(displayName), "five hour"):
		winSec = 18000
		winSuffix = "5h"
		winOrder = 1
	case winLower == "weekly" || strings.Contains(strings.ToLower(bucketID), "weekly") || strings.Contains(strings.ToLower(displayName), "weekly"):
		winSec = 604800
		winSuffix = "周限额"
		winOrder = 2
	case winLower == "daily" || winLower == "24h":
		winSec = 86400
		winSuffix = "24h"
		winOrder = 1
	default:
		rst := parseFlexibleTime(resetStr)
		winSec = inferGoogleWindowSeconds(rst)
	}

	label := baseLabel
	if winSuffix != "" {
		label = fmt.Sprintf("%s (%s)", baseLabel, winSuffix)
	}
	key := coalesceStr(strings.ReplaceAll(strings.ToLower(bucketID), "-", "_"), family+"_"+winLower)

	return summaryWindowItem{
		Family:      family,
		Key:         key,
		Label:       label,
		Order:       familyBaseOrder + winOrder,
		UsedPercent: clampPercent((1.0 - rem) * 100.0),
		WindowSec:   winSec,
		ResetTime:   parseFlexibleTime(resetStr),
	}, true
}

func extractSummaryItemsFromGroups(groups []rawQuotaSummaryGroup, topBuckets []rawQuotaSummaryBucket) []summaryWindowItem {
	var out []summaryWindowItem
	for _, g := range groups {
		gName := coalesceStr(g.DisplayName, g.Name)
		for _, b := range g.Buckets {
			if item, ok := convertQuotaSummaryBucket(gName, b); ok {
				out = append(out, item)
			}
		}
	}
	if len(out) == 0 {
		for _, b := range topBuckets {
			if item, ok := convertQuotaSummaryBucket("", b); ok {
				out = append(out, item)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Order < out[j].Order
	})
	return out
}

func parseRetrieveUserQuotaSummary(raw []byte) []summaryWindowItem {
	var doc struct {
		Buckets []rawQuotaSummaryBucket `json:"buckets"`
		Groups  []rawQuotaSummaryGroup  `json:"groups"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return extractSummaryItemsFromGroups(doc.Groups, doc.Buckets)
}

func parseAICodeFetchQuotaStatus(raw []byte) ([]summaryWindowItem, []rawModelQuotaItem, int64) {
	var doc struct {
		QuotaSummary *struct {
			Groups  []rawQuotaSummaryGroup  `json:"groups"`
			Buckets []rawQuotaSummaryBucket `json:"buckets"`
		} `json:"quotaSummary"`
		AvailableCredits []struct {
			CreditType   string `json:"creditType"`
			CreditAmount any    `json:"creditAmount"`
		} `json:"availableCredits"`
		Buckets []struct {
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
			Model             *struct {
				ModelID     string `json:"modelId"`
				DisplayName string `json:"displayName"`
			} `json:"model"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, 0
	}

	var summaryItems []summaryWindowItem
	if doc.QuotaSummary != nil {
		summaryItems = extractSummaryItemsFromGroups(doc.QuotaSummary.Groups, doc.QuotaSummary.Buckets)
	}

	var credits int64
	for _, c := range doc.AvailableCredits {
		if f, ok := parseFlexibleFloat(c.CreditAmount); ok && f > 0 {
			credits = int64(math.Round(f))
			break
		}
	}

	var modelItems []rawModelQuotaItem
	for _, b := range doc.Buckets {
		if b.Model == nil || strings.TrimSpace(b.Model.ModelID) == "" {
			continue
		}
		rem := 1.0
		if b.RemainingFraction != nil {
			rem = *b.RemainingFraction
		} else if b.ResetTime != "" {
			rem = 0.0
		}
		if rem < 0 {
			rem = 0
		}
		if rem > 1 {
			rem = 1
		}
		modelItems = append(modelItems, rawModelQuotaItem{
			ModelID:           b.Model.ModelID,
			DisplayName:       b.Model.DisplayName,
			RemainingFraction: rem,
			ResetTime:         parseFlexibleTime(b.ResetTime),
		})
	}
	return summaryItems, modelItems, credits
}

func parseAgyCLIUsageJSON(raw []byte) []summaryWindowItem {
	var doc struct {
		Status  string `json:"status"`
		Command *struct {
			Name string `json:"name"`
			Data *struct {
				Groups []rawQuotaSummaryGroup `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	if !strings.EqualFold(doc.Status, "SUCCESS") || doc.Command == nil || doc.Command.Data == nil {
		return nil
	}
	return extractSummaryItemsFromGroups(doc.Command.Data.Groups, nil)
}

func tryLocalAgyUsageSummary(ctx context.Context, ccpaSummary []summaryWindowItem, ccpaModels []rawModelQuotaItem, accountEmail string) []summaryWindowItem {
	runner := agyCLIUsageRunner
	if runner == nil {
		if !isUsingDefaultHTTPClient() {
			return nil
		}
		runner = runLocalAgyCLIUsage
	}
	out, err := runner(ctx)
	if err != nil || len(out) == 0 {
		return nil
	}
	cliItems := parseAgyCLIUsageJSON(out)
	if len(cliItems) == 0 {
		return nil
	}
	if !agyResetTimestampsAlign(cliItems, ccpaSummary, ccpaModels, accountEmail) {
		return nil
	}
	return cliItems
}

func runLocalAgyCLIUsage(ctx context.Context) ([]byte, error) {
	binPath := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cand := filepath.Join(home, ".local", "bin", "agy")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			binPath = cand
		}
	}
	if binPath == "" {
		for _, cand := range []string{"/opt/homebrew/bin/agy", "/usr/local/bin/agy"} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				binPath = cand
				break
			}
		}
	}
	if binPath == "" {
		if p, err := exec.LookPath("agy"); err == nil {
			binPath = p
		}
	}
	if binPath == "" {
		return nil, errors.New("agy binary not found")
	}

	cctx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, binPath, "-p", "/usage", "--output-format", "json", "--print-timeout", "12s")
	return cmd.Output()
}

func agyResetTimestampsAlign(cliItems []summaryWindowItem, ccpaSummary []summaryWindowItem, ccpaModels []rawModelQuotaItem, accountEmail string) bool {
	if strings.TrimSpace(accountEmail) == "" {
		return true
	}
	var refs []*time.Time
	for _, s := range ccpaSummary {
		if s.ResetTime != nil {
			refs = append(refs, s.ResetTime)
		}
	}
	for _, m := range ccpaModels {
		if m.ResetTime != nil {
			refs = append(refs, m.ResetTime)
		}
	}
	if len(refs) == 0 {
		return false
	}
	for _, ci := range cliItems {
		if ci.ResetTime == nil {
			continue
		}
		for _, r := range refs {
			diff := ci.ResetTime.Sub(*r)
			if diff < 0 {
				diff = -diff
			}
			if diff <= 2*time.Minute {
				return true
			}
		}
	}
	return false
}

func hasSummaryFamily(items []summaryWindowItem, family string) bool {
	for _, it := range items {
		if it.Family == family {
			return true
		}
	}
	return false
}

func hasSummaryWindowKey(items []summaryWindowItem, key string) bool {
	for _, it := range items {
		if it.Key == key {
			return true
		}
	}
	return false
}

func mergeAntigravityWindows(summaryItems []summaryWindowItem, modelItems []rawModelQuotaItem) []model.QuotaWindow {
	modelWins := groupGoogleModelQuotas(modelItems)
	if len(summaryItems) == 0 {
		return modelWins
	}

	hasFamilyInSummary := map[string]bool{}
	for _, s := range summaryItems {
		hasFamilyInSummary[s.Family] = true
	}
	authoritativeGeminiSummary := hasSummaryWindowKey(summaryItems, "gemini_weekly")

	// Cross-check 5h CCPA summary buckets with live per-model quota fractions when summary did not come from AICode's unified groups.
	maxModelUsedByFamily := map[string]float64{}
	modelResetByFamily := map[string]*time.Time{}
	for _, mw := range modelWins {
		fam := "other"
		if strings.HasPrefix(mw.Key, "gemini") || mw.Key == "flash_lite" {
			fam = "gemini"
		} else if mw.Key == "claude" || mw.Key == "gpt" {
			fam = "3p"
		}
		if mw.UsedPercent > maxModelUsedByFamily[fam] {
			maxModelUsedByFamily[fam] = mw.UsedPercent
			modelResetByFamily[fam] = mw.ResetsAt
		}
	}

	type orderedWin struct {
		order int
		win   model.QuotaWindow
	}
	var merged []orderedWin

	for _, s := range summaryItems {
		used := s.UsedPercent
		rst := s.ResetTime
		if s.WindowSec <= 18000 && !(authoritativeGeminiSummary && s.Family == "gemini") {
			if mUsed, ok := maxModelUsedByFamily[s.Family]; ok && mUsed > used {
				used = mUsed
				if rst == nil {
					rst = modelResetByFamily[s.Family]
				}
			}
		}
		merged = append(merged, orderedWin{
			order: s.Order,
			win: makeWindow(
				s.Key,
				s.Label,
				used,
				rst,
				s.WindowSec,
				fmt.Sprintf("已用 %.0f%%", used),
			),
		})
	}

	// Include model-level families omitted by retrieveUserQuotaSummary (e.g., Gemini pool on CCPA-only responses).
	for idx, mw := range modelWins {
		fam := "other"
		order := 30 + idx
		if strings.HasPrefix(mw.Key, "gemini") || mw.Key == "flash_lite" {
			fam = "gemini"
			order = 1 + idx
		} else if mw.Key == "claude" || mw.Key == "gpt" {
			fam = "3p"
			order = 10 + idx
		}
		if !hasFamilyInSummary[fam] {
			merged = append(merged, orderedWin{order: order, win: mw})
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].order < merged[j].order
	})
	out := make([]model.QuotaWindow, 0, len(merged))
	for _, m := range merged {
		out = append(out, m.win)
	}
	return out
}

func normalizeGoogleTier(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "ultra"):
		return "AI Ultra"
	case strings.Contains(lower, "pro"):
		return "AI Pro"
	case strings.Contains(lower, "standard"):
		return "Standard"
	case strings.Contains(lower, "free") || strings.Contains(lower, "legacy"):
		return "Free"
	default:
		if len(s) > 20 {
			return s[:20]
		}
		return s
	}
}

func parseFetchAvailableModels(raw []byte) []rawModelQuotaItem {
	var doc struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			QuotaInfo   *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
				IsExhausted       bool     `json:"isExhausted"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Models) == 0 {
		return nil
	}
	out := make([]rawModelQuotaItem, 0, len(doc.Models))
	for modelID, m := range doc.Models {
		if m.QuotaInfo == nil {
			continue
		}
		qi := m.QuotaInfo
		if qi.RemainingFraction == nil && qi.ResetTime == "" && !qi.IsExhausted {
			continue
		}
		rem := 1.0
		if qi.IsExhausted {
			rem = 0.0
		} else if qi.RemainingFraction != nil {
			rem = *qi.RemainingFraction
		} else if qi.ResetTime != "" {
			// Proto3 JSON omits 0.0 when remainingFraction hits exact zero during active reset window.
			rem = 0.0
		}
		if rem < 0 {
			rem = 0
		}
		if rem > 1 {
			rem = 1
		}
		out = append(out, rawModelQuotaItem{
			ModelID:           modelID,
			DisplayName:       m.DisplayName,
			RemainingFraction: rem,
			ResetTime:         parseFlexibleTime(qi.ResetTime),
		})
	}
	return out
}

func parseRetrieveUserQuota(raw []byte) []rawModelQuotaItem {
	var doc struct {
		Buckets []struct {
			ModelID           string   `json:"modelId"`
			TokenType         string   `json:"tokenType"`
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Buckets) == 0 {
		return nil
	}
	out := make([]rawModelQuotaItem, 0, len(doc.Buckets))
	for _, b := range doc.Buckets {
		if strings.TrimSpace(b.ModelID) == "" {
			continue
		}
		rem := 1.0
		if b.RemainingFraction != nil {
			rem = *b.RemainingFraction
		} else if b.ResetTime != "" {
			rem = 0.0
		}
		if rem < 0 {
			rem = 0
		}
		if rem > 1 {
			rem = 1
		}
		out = append(out, rawModelQuotaItem{
			ModelID:           b.ModelID,
			RemainingFraction: rem,
			ResetTime:         parseFlexibleTime(b.ResetTime),
		})
	}
	return out
}

func groupGoogleModelQuotas(items []rawModelQuotaItem) []model.QuotaWindow {
	if len(items) == 0 {
		return nil
	}

	type bucketAgg struct {
		key       string
		label     string
		order     int
		usedPct   float64
		resetsAt  *time.Time
		populated bool
	}

	hasStandardFlash := false
	for _, it := range items {
		id := strings.ToLower(it.ModelID)
		if strings.Contains(id, "gemini") && strings.Contains(id, "flash") && !strings.Contains(id, "lite") && !strings.Contains(id, "image") {
			hasStandardFlash = true
			break
		}
	}

	agg := map[string]*bucketAgg{
		"gemini_pro":   {key: "gemini_pro", label: "Gemini Pro", order: 1},
		"gemini_flash": {key: "gemini_flash", label: "Gemini Flash", order: 2},
		"flash_lite":   {key: "flash_lite", label: "Flash Lite", order: 3},
		"claude":       {key: "claude", label: "Claude 配额", order: 4},
		"gpt":          {key: "gpt", label: "GPT 配额", order: 5},
	}

	for _, it := range items {
		id := strings.ToLower(it.ModelID)
		// Skip internal autocomplete / chat alias / image / embedding buckets
		if strings.HasPrefix(id, "tab_") || strings.HasPrefix(id, "chat_") ||
			strings.Contains(id, "embedding") || strings.Contains(id, "imagen") ||
			strings.Contains(id, "veo") || strings.Contains(id, "image") {
			continue
		}

		var targetKey string
		switch {
		case strings.Contains(id, "claude"):
			targetKey = "claude"
		case strings.Contains(id, "gpt") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4"):
			targetKey = "gpt"
		case strings.Contains(id, "gemini") && strings.Contains(id, "flash") && strings.Contains(id, "lite"):
			if hasStandardFlash {
				targetKey = "flash_lite"
			} else {
				targetKey = "gemini_flash"
			}
		case strings.Contains(id, "gemini") && strings.Contains(id, "flash"):
			targetKey = "gemini_flash"
		case strings.Contains(id, "gemini"):
			targetKey = "gemini_pro"
		default:
			continue
		}

		used := clampPercent((1.0 - it.RemainingFraction) * 100.0)
		b := agg[targetKey]
		if !b.populated || used > b.usedPct || (used == b.usedPct && b.resetsAt == nil && it.ResetTime != nil) {
			b.usedPct = used
			b.resetsAt = it.ResetTime
			b.populated = true
		}
	}

	// Merge Gemini Pro & Flash into a unified Gemini pool when they share the exact same bucket (same used% & resetTime),
	// matching `agy` CLI `/usage`'s unified "Gemini models" grouping; keep them separate if their limits differ.
	gPro := agg["gemini_pro"]
	gFlash := agg["gemini_flash"]
	if gPro.populated && gFlash.populated && math.Abs(gPro.usedPct-gFlash.usedPct) <= 0.2 && sameResetBucket(gPro.resetsAt, gFlash.resetsAt) {
		gPro.key = "gemini_5h"
		if inferGoogleWindowSeconds(gPro.resetsAt) == 18000 {
			gPro.label = "Gemini (5h)"
		} else {
			gPro.label = "Gemini"
		}
		gFlash.populated = false
		if agg["flash_lite"].populated && math.Abs(gPro.usedPct-agg["flash_lite"].usedPct) <= 0.2 && sameResetBucket(gPro.resetsAt, agg["flash_lite"].resetsAt) {
			agg["flash_lite"].populated = false
		}
	}

	// Merge GPT into Claude when Antigravity shares a single third-party pool (same used% & resetTime).
	cl := agg["claude"]
	gp := agg["gpt"]
	if cl.populated && gp.populated && math.Abs(cl.usedPct-gp.usedPct) <= 0.2 && sameResetBucket(cl.resetsAt, gp.resetsAt) {
		cl.label = "Claude / GPT"
		gp.populated = false
	}

	// Hide Flash Lite if untouched (0% used) while another Gemini bucket is already shown, keeping cards concise.
	if (agg["gemini_pro"].populated || agg["gemini_flash"].populated) && agg["flash_lite"].populated && agg["flash_lite"].usedPct < 0.5 {
		agg["flash_lite"].populated = false
	}

	list := make([]*bucketAgg, 0, len(agg))
	for _, b := range agg {
		if b.populated {
			list = append(list, b)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })

	windows := make([]model.QuotaWindow, 0, len(list))
	for _, b := range list {
		winSec := inferGoogleWindowSeconds(b.resetsAt)
		windows = append(windows, makeWindow(
			b.key,
			b.label,
			b.usedPct,
			b.resetsAt,
			winSec,
			fmt.Sprintf("已用 %.0f%%", b.usedPct),
		))
	}
	return windows
}

func sameResetBucket(a, b *time.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	diff := a.Sub(*b)
	if diff < 0 {
		diff = -diff
	}
	return diff <= 10*time.Second
}

func inferGoogleWindowSeconds(resetsAt *time.Time) int64 {
	if resetsAt != nil {
		rem := time.Until(*resetsAt)
		if rem > 0 && rem <= 5*time.Hour+10*time.Minute {
			return 18000 // 5h rolling window
		}
		if rem > 0 && rem <= 24*time.Hour+15*time.Minute {
			return 86400 // 24h rolling window
		}
		if rem > 24*time.Hour+15*time.Minute {
			return 604800 // 7d window
		}
	}
	return 18000
}
