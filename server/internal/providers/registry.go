package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
)

type TokenUpdate struct {
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt *time.Time
	AccountEmail   string
	AccountID      string
	ProjectID      string
}

type FetchResult struct {
	PlanLabel     string
	AccountEmail  string
	PrimaryMetric string
	Status        model.StatusLevel
	StatusReason  string
	Windows       []model.QuotaWindow
	Balance       *model.BalanceInfo
	TokenUpdate   *TokenUpdate
}

type Provider interface {
	Descriptor() model.ProviderDescriptor
	Fetch(ctx context.Context, cfg model.EntryConfig) (FetchResult, error)
}

type Registry struct {
	order []string
	byID  map[string]Provider
}

func NewRegistry() *Registry {
	r := &Registry{
		byID: make(map[string]Provider),
	}
	list := []Provider{
		&antigravityProvider{},
		&codexProvider{},
		&claudeProvider{},
		&kimiSubscriptionProvider{},
		&deepseekProvider{},
		&openrouterProvider{},
		&moonshotProvider{},
		&siliconflowProvider{},
		&openaiBillingProvider{},
		&customHTTPProvider{},
		&manualProvider{},
	}
	for _, p := range list {
		id := p.Descriptor().ID
		r.order = append(r.order, id)
		r.byID[id] = p
	}
	return r
}

func (r *Registry) Descriptors() []model.ProviderDescriptor {
	out := make([]model.ProviderDescriptor, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id].Descriptor())
	}
	return out
}

func (r *Registry) NamesMap() map[string]string {
	m := make(map[string]string, len(r.order))
	for _, id := range r.order {
		m[id] = r.byID[id].Descriptor().Name
	}
	return m
}

func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.byID[id]
	return p, ok
}

var defaultHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
	},
}

var sharedHTTPClient = defaultHTTPClient

func isUsingDefaultHTTPClient() bool {
	return sharedHTTPClient == defaultHTTPClient
}

// SetHTTPTransportForTest overrides the outbound HTTP transport for unit tests and returns a restore func.
func SetHTTPTransportForTest(rt http.RoundTripper) func() {
	orig := sharedHTTPClient
	sharedHTTPClient = &http.Client{Transport: rt}
	return func() { sharedHTTPClient = orig }
}

func doJSONRequest(ctx context.Context, method, rawURL string, headers map[string]string) ([]byte, int, error) {
	return doRawRequest(ctx, method, rawURL, headers, nil)
}

func doJSONBodyRequest(ctx context.Context, method, rawURL string, headers map[string]string, payload any) ([]byte, int, error) {
	var rawBody []byte
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("序列化请求失败: %w", err)
		}
		rawBody = b
	}
	hdrs := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		hdrs[k] = v
	}
	if _, ok := hdrs["Content-Type"]; !ok {
		hdrs["Content-Type"] = "application/json"
	}
	return doRawRequest(ctx, method, rawURL, hdrs, rawBody)
}

func doRawRequest(ctx context.Context, method, rawURL string, headers map[string]string, reqBody []byte) ([]byte, int, error) {
	if method == "" {
		method = http.MethodGet
	}
	var rdr io.Reader
	if len(reqBody) > 0 {
		rdr = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, 0, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if k != "" && v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取响应失败: %w", err)
	}
	return body, resp.StatusCode, nil
}

// resolveSafeHomeJSONPath restricts local credential reading to .json files inside $HOME.
func resolveSafeHomeJSONPath(inputPath, defaultRel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("无法解析用户主目录")
	}
	homeClean, err := filepath.EvalSymlinks(home)
	if err != nil {
		homeClean = filepath.Clean(home)
	}

	p := strings.TrimSpace(inputPath)
	if p == "" {
		p = filepath.Join(homeClean, defaultRel)
	} else if p == "~" {
		p = homeClean
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(homeClean, p[2:])
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(homeClean, p)
	}

	clean := filepath.Clean(p)
	if strings.ToLower(filepath.Ext(clean)) != ".json" {
		return "", errors.New("凭据文件路径仅允许 .json 文件")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("凭据文件不存在: %s", clean)
		}
		return "", fmt.Errorf("读取凭据路径失败: %w", err)
	}
	rel, err := filepath.Rel(homeClean, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("出于安全边界限制，凭据文件必须位于当前用户主目录内")
	}
	return resolved, nil
}

func makeWindow(key, label string, usedPct float64, resetsAt *time.Time, windowSec int64, usedDisplay string) model.QuotaWindow {
	usedPct = clampPercent(usedPct)
	remPct := math.Round((100.0-usedPct)*10) / 10
	usedPct = math.Round(usedPct*10) / 10

	var pace *float64
	if resetsAt != nil && windowSec > 0 {
		remainingSec := time.Until(*resetsAt).Seconds()
		if remainingSec >= 0 && remainingSec <= float64(windowSec) {
			elapsedPct := ((float64(windowSec) - remainingSec) / float64(windowSec)) * 100.0
			delta := math.Round((usedPct-elapsedPct)*10) / 10
			pace = &delta
		}
	}
	return model.QuotaWindow{
		Key:              key,
		Label:            label,
		UsedPercent:      usedPct,
		RemainingPercent: remPct,
		UsedDisplay:      usedDisplay,
		ResetsAt:         resetsAt,
		WindowSeconds:    windowSec,
		PaceDeltaPercent: pace,
	}
}

func evaluateWindowsStatus(windows []model.QuotaWindow, warnThreshold float64) (model.StatusLevel, string, string) {
	if len(windows) == 0 {
		return model.StatusOK, "", "正常"
	}
	if warnThreshold <= 0 {
		warnThreshold = 75
	}
	critThreshold := math.Max(warnThreshold+10, 90)
	if critThreshold > 98 {
		critThreshold = 98
	}

	worst := model.StatusOK
	reason := ""
	maxUsed := -1.0
	primaryIdx := 0

	for i, w := range windows {
		if w.UsedPercent > maxUsed {
			maxUsed = w.UsedPercent
			primaryIdx = i
		}
		if w.UsedPercent >= 100 {
			worst = model.StatusCritical
			reason = fmt.Sprintf("%s 已耗尽", w.Label)
		} else if w.UsedPercent >= critThreshold && worst != model.StatusCritical {
			worst = model.StatusCritical
			reason = fmt.Sprintf("%s 已用 %.0f%%", w.Label, w.UsedPercent)
		} else if w.UsedPercent >= warnThreshold && worst == model.StatusOK {
			worst = model.StatusWarning
			reason = fmt.Sprintf("%s 已用 %.0f%%", w.Label, w.UsedPercent)
		}
	}
	pw := windows[primaryIdx]
	primary := fmt.Sprintf("剩余 %.0f%%", pw.RemainingPercent)
	return worst, reason, primary
}

func evaluateBalanceStatus(b *model.BalanceInfo) (model.StatusLevel, string, string) {
	if b == nil {
		return model.StatusOK, "", "--"
	}
	primary := fmt.Sprintf("%s%.2f", b.Symbol, b.Available)
	if b.IsAvailable != nil && !*b.IsAvailable {
		return model.StatusCritical, "余额不可用", primary
	}
	if b.Available <= 0 {
		// Special case: unlimited pay-as-you-go key where Available represents 0 limit_remaining? Handled by provider.
		return model.StatusCritical, "余额已用尽", primary
	}
	if b.LowThreshold != nil && *b.LowThreshold > 0 {
		if b.Available <= *b.LowThreshold*0.3 {
			return model.StatusCritical, fmt.Sprintf("低于预警线 %s%.2f", b.Symbol, *b.LowThreshold), primary
		}
		if b.Available <= *b.LowThreshold {
			return model.StatusWarning, fmt.Sprintf("低于预警线 %s%.2f", b.Symbol, *b.LowThreshold), primary
		}
	}
	return model.StatusOK, "", primary
}

func clampPercent(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func parseFlexibleFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		s := strings.TrimSpace(strings.TrimLeft(x, "$¥€ "))
		s = strings.ReplaceAll(s, ",", "")
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func parseFlexibleTime(v any) *time.Time {
	switch x := v.(type) {
	case float64:
		return unixToTime(int64(x))
	case int64:
		return unixToTime(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return unixToTime(n)
		}
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixToTime(n)
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil {
				u := t.UTC()
				return &u
			}
		}
	}
	return nil
}

func unixToTime(n int64) *time.Time {
	if n <= 0 {
		return nil
	}
	// Distinguish milliseconds vs seconds
	if n > 1e12 {
		t := time.UnixMilli(n).UTC()
		return &t
	}
	t := time.Unix(n, 0).UTC()
	return &t
}

func truncateErrBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// DiscoverLocalHints checks well-known local CLI auth locations without reading/exposing secrets or paths.
func DiscoverLocalHints() []model.DiscoveryHint {
	var hints []model.DiscoveryHint
	home, err := os.UserHomeDir()
	if err != nil {
		return hints
	}

	codexAuth := filepath.Join(home, ".codex", "auth.json")
	if info, err := os.Stat(codexAuth); err == nil && !info.IsDir() {
		hints = append(hints, model.DiscoveryHint{
			ID:            "codex_local",
			Provider:      "codex_oauth",
			SuggestedName: "OpenAI Codex",
			Note:          "检测到本机已登录的 Codex 账号，支持免浏览器一键同步",
		})
	}

	claudeFile := filepath.Join(home, ".claude", ".credentials.json")
	if info, err := os.Stat(claudeFile); err == nil && !info.IsDir() {
		hints = append(hints, model.DiscoveryHint{
			ID:            "claude_local",
			Provider:      "claude_oauth",
			SuggestedName: "Claude Code",
			Note:          "检测到本机已登录的 Claude Code 账号，支持免浏览器一键同步",
		})
	} else if runtime.GOOS == "darwin" {
		// Non-interactive metadata check only (-s without -w does not print the secret).
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "Claude Code-credentials")
		if err := cmd.Run(); err == nil {
			hints = append(hints, model.DiscoveryHint{
				ID:            "claude_local",
				Provider:      "claude_oauth",
				SuggestedName: "Claude Code",
				Note:          "检测到本机已登录的 Claude Code 账号，支持免浏览器一键同步",
			})
		}
	}

	kimiCred := filepath.Join(home, ".kimi", "credentials", "kimi-code.json")
	if info, err := os.Stat(kimiCred); err == nil && !info.IsDir() {
		hints = append(hints, model.DiscoveryHint{
			ID:            "kimi_local",
			Provider:      "kimi_oauth",
			SuggestedName: "Kimi 订阅",
			Note:          "检测到本机已登录的 Kimi CLI 账号，支持免浏览器一键同步",
		})
	}

	return hints
}
