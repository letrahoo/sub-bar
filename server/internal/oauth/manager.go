package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
	"github.com/letrahoo/sub-bar/server/internal/poller"
	"github.com/letrahoo/sub-bar/server/internal/providers"
	"github.com/letrahoo/sub-bar/server/internal/store"
)

const sessionTTL = 5 * time.Minute

var devicePollIntervalOverride time.Duration

// SetDevicePollIntervalForTest speeds up RFC 8628 device-code polling in unit tests.
func SetDevicePollIntervalForTest(d time.Duration) func() {
	prev := devicePollIntervalOverride
	devicePollIntervalOverride = d
	return func() { devicePollIntervalOverride = prev }
}

type StartRequest struct {
	Provider       string   `json:"provider"`
	Name           string   `json:"name"`
	Badge          string   `json:"badge"`
	Pinned         bool     `json:"pinned"`
	Enabled        *bool    `json:"enabled"`
	RefreshSeconds int      `json:"refreshSeconds"`
	MonthlyCost    *float64 `json:"monthlyCost"`
	CostCurrency   string   `json:"costCurrency"`
	DashboardURL   string   `json:"dashboardUrl"`
	Notes          string   `json:"notes"`
	PlanLabel      string   `json:"planLabel"`
	AccountID      string   `json:"accountId"`
	BaseURL        string   `json:"baseUrl"`
	EntryID        string   `json:"entryId"`
}

type StartResponse struct {
	State        string    `json:"state"`
	Provider     string    `json:"provider"`
	AuthURL      string    `json:"authUrl"`
	CallbackPort int       `json:"callbackPort"`
	CallbackMode string    `json:"callbackMode"` // "loopback" | "manual" | "device"
	UserCode     string    `json:"userCode,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type StatusResponse struct {
	State        string                   `json:"state"`
	Provider     string                   `json:"provider"`
	Status       string                   `json:"status"` // "pending" | "exchanging" | "completed" | "error" | "expired"
	Error        string                   `json:"error,omitempty"`
	AccountEmail string                   `json:"accountEmail,omitempty"`
	Entry        *model.EntryConfigPublic `json:"entry,omitempty"`
	Snapshot     *model.EntrySnapshot     `json:"snapshot,omitempty"`
}

type session struct {
	state        string
	verifier     string
	redirectURI  string
	port         int
	userCode     string
	cancel       context.CancelFunc
	req          StartRequest
	expiresAt    time.Time
	status       string
	errMsg       string
	accountEmail string
	entry        *model.EntryConfigPublic
	snapshot     *model.EntrySnapshot
}

type portServer struct {
	port    int
	servers []*http.Server
}

func (ps *portServer) Close() {
	for _, s := range ps.servers {
		_ = s.Close()
	}
}

type Manager struct {
	store    *store.Store
	registry *providers.Registry
	poller   *poller.Poller

	mu        sync.Mutex
	sessions  map[string]*session
	listeners map[int]*portServer
}

func NewManager(st *store.Store, reg *providers.Registry, pl *poller.Poller) *Manager {
	return &Manager{
		store:     st,
		registry:  reg,
		poller:    pl,
		sessions:  make(map[string]*session),
		listeners: make(map[int]*portServer),
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sess := range m.sessions {
		if sess.cancel != nil {
			sess.cancel()
		}
	}
	for port, ps := range m.listeners {
		ps.Close()
		delete(m.listeners, port)
	}
}

func (m *Manager) Start(req StartRequest) (StartResponse, error) {
	spec, ok := providers.GetOAuthSpec(strings.TrimSpace(req.Provider))
	if !ok {
		return StartResponse{}, fmt.Errorf("该服务不支持浏览器 OAuth 一键登录: %s", req.Provider)
	}
	if st, _ := providers.OAuthClientStatusFor(strings.TrimSpace(req.Provider)); !st.Configured {
		return StartResponse{}, fmt.Errorf("OAuth_CLIENT_REQUIRED: 尚未填写 %s 的 OAuth 客户端凭据，请先在授权面板中填写", req.Provider)
	}

	state, err := randomHex(16)
	if err != nil {
		return StartResponse{}, err
	}

	if spec.DeviceFlow {
		return m.startDeviceSession(spec, state, req)
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return StartResponse{}, err
	}

	redirectURI := spec.DefaultRedirectURI()
	authURL := providers.BuildAuthURL(spec, state, challenge, redirectURI)
	expiresAt := time.Now().UTC().Add(sessionTTL)

	m.mu.Lock()
	callbackMode := "loopback"
	if err := m.ensurePortListenerLocked(spec.CallbackPort); err != nil {
		// Port occupied by another local CLI tool; paste-callback fallback still works seamlessly.
		callbackMode = "manual"
	}

	m.sessions[state] = &session{
		state:       state,
		verifier:    verifier,
		redirectURI: redirectURI,
		port:        spec.CallbackPort,
		req:         req,
		expiresAt:   expiresAt,
		status:      "pending",
	}
	m.mu.Unlock()

	time.AfterFunc(sessionTTL+3*time.Second, m.gc)

	return StartResponse{
		State:        state,
		Provider:     spec.ProviderID,
		AuthURL:      authURL,
		CallbackPort: spec.CallbackPort,
		CallbackMode: callbackMode,
		ExpiresAt:    expiresAt,
	}, nil
}

func (m *Manager) startDeviceSession(spec providers.OAuthSpec, state string, req StartRequest) (StartResponse, error) {
	dCtx, dCancel := context.WithTimeout(context.Background(), 12*time.Second)
	dev, err := providers.StartDeviceOAuth(dCtx, spec.ProviderID, req.BaseURL)
	dCancel()
	if err != nil {
		return StartResponse{}, err
	}

	ttl := sessionTTL
	if dev.ExpiresIn > 0 {
		cand := time.Duration(dev.ExpiresIn) * time.Second
		if cand < 2*time.Minute {
			cand = 2 * time.Minute
		} else if cand > 15*time.Minute {
			cand = 15 * time.Minute
		}
		ttl = cand
	}
	expiresAt := time.Now().UTC().Add(ttl)

	pollCtx, pollCancel := context.WithDeadline(context.Background(), expiresAt)
	sess := &session{
		state:     state,
		userCode:  dev.UserCode,
		cancel:    pollCancel,
		req:       req,
		expiresAt: expiresAt,
		status:    "pending",
	}

	m.mu.Lock()
	m.sessions[state] = sess
	m.mu.Unlock()

	go m.pollDeviceSession(pollCtx, state, dev)
	time.AfterFunc(ttl+3*time.Second, m.gc)

	return StartResponse{
		State:        state,
		Provider:     spec.ProviderID,
		AuthURL:      dev.VerificationURIComplete,
		CallbackPort: 0,
		CallbackMode: "device",
		UserCode:     dev.UserCode,
		ExpiresAt:    expiresAt,
	}, nil
}

func (m *Manager) pollDeviceSession(ctx context.Context, state string, dev providers.DeviceAuthSession) {
	interval := 2500 * time.Millisecond
	if dev.Interval > 0 && dev.Interval <= 3 {
		interval = time.Duration(dev.Interval) * time.Second
	}
	if devicePollIntervalOverride > 0 {
		interval = devicePollIntervalOverride
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		m.mu.Lock()
		sess, ok := m.sessions[state]
		if !ok || sess.status != "pending" {
			m.mu.Unlock()
			return
		}
		if time.Now().UTC().After(sess.expiresAt) {
			sess.status = "expired"
			sess.errMsg = "等待网页/微信扫码确认超时，请重新点击生成链接"
			m.mu.Unlock()
			return
		}
		reqCopy := sess.req
		m.mu.Unlock()

		pCtx, pCancel := context.WithTimeout(ctx, 10*time.Second)
		upd, err := providers.PollDeviceOAuthToken(pCtx, reqCopy.Provider, dev.AuthBaseURL, dev.DeviceCode, dev.DeviceID)
		pCancel()

		if errors.Is(err, providers.ErrDeviceAuthPending) {
			timer.Reset(interval)
			continue
		}
		if errors.Is(err, providers.ErrDeviceAuthSlowDown) {
			interval += 4 * time.Second
			timer.Reset(interval)
			continue
		}
		if err != nil {
			// Transient network glitch vs explicit OAuth terminal error
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "网络请求失败") {
				timer.Reset(interval)
				continue
			}
			m.mu.Lock()
			if s, exists := m.sessions[state]; exists && s.status == "pending" {
				s.status = "error"
				s.errMsg = err.Error()
			}
			m.mu.Unlock()
			return
		}

		m.mu.Lock()
		sess, ok = m.sessions[state]
		if !ok || sess.status != "pending" {
			m.mu.Unlock()
			return
		}
		sess.status = "exchanging"
		m.mu.Unlock()

		_ = m.finalizeAuthorizedSession(sess, reqCopy, upd)
		return
	}
}

func (m *Manager) Status(state string) (StatusResponse, error) {
	key := strings.TrimSpace(state)
	if key == "" {
		return StatusResponse{}, errors.New("缺少 state 参数")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[key]
	if !ok {
		return StatusResponse{}, errors.New("授权会话不存在或已过期")
	}
	if sess.status == "pending" && time.Now().UTC().After(sess.expiresAt) {
		sess.status = "expired"
		sess.errMsg = "等待授权超时（5分钟），请重新点击生成链接"
	}

	return StatusResponse{
		State:        sess.state,
		Provider:     sess.req.Provider,
		Status:       sess.status,
		Error:        sess.errMsg,
		AccountEmail: sess.accountEmail,
		Entry:        sess.entry,
		Snapshot:     sess.snapshot,
	}, nil
}

// SubmitCallbackInput accepts either a full redirected URL (http://localhost:51121/oauth-callback?code=...&state=...)
// or a raw authorization code for cross-device login.
func (m *Manager) SubmitCallbackInput(ctx context.Context, expectedState, rawInput string) (StatusResponse, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return StatusResponse{}, errors.New("请粘贴完整的回调地址或授权码")
	}

	state := strings.TrimSpace(expectedState)
	code := ""
	errParam := ""
	errDesc := ""

	if strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "/oauth") || strings.HasPrefix(trimmed, "/auth") || strings.HasPrefix(trimmed, "/callback") || strings.Contains(trimmed, "code=") {
		parseTarget := trimmed
		if !strings.Contains(parseTarget, "://") && !strings.HasPrefix(parseTarget, "/") {
			parseTarget = "http://localhost/?" + strings.TrimPrefix(parseTarget, "?")
		}
		u, err := url.Parse(parseTarget)
		if err != nil {
			return StatusResponse{}, errors.New("回调链接格式无效，请粘贴浏览器地址栏的完整链接")
		}
		q := u.Query()
		code = q.Get("code")
		if urlState := strings.TrimSpace(q.Get("state")); urlState != "" {
			if state != "" && state != urlState {
				return StatusResponse{}, errors.New("回调链接中的 state 与当前会话不匹配，请使用最新生成的链接")
			}
			state = urlState
		}
		errParam = q.Get("error")
		errDesc = q.Get("error_description")
	} else {
		// Raw code (or Anthropic code#state)
		code = trimmed
		if idx := strings.IndexByte(trimmed, '#'); idx > 0 {
			code = trimmed[:idx]
			if state == "" {
				state = trimmed[idx+1:]
			}
		}
	}

	if state == "" {
		return StatusResponse{}, errors.New("无法识别授权会话 state")
	}
	if err := m.completeSession(ctx, state, code, errParam, errDesc); err != nil {
		return StatusResponse{}, err
	}
	return m.Status(state)
}

// CallbackHandler handles loopback browser redirects on 127.0.0.1 / [::1].
// Exposed so tests can also exercise browser callback handling directly.
func (m *Manager) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		q := r.URL.Query()
		state := strings.TrimSpace(q.Get("state"))
		code := strings.TrimSpace(q.Get("code"))
		errParam := strings.TrimSpace(q.Get("error"))
		errDesc := strings.TrimSpace(q.Get("error_description"))

		if state == "" {
			renderCallbackHTML(w, http.StatusBadRequest, false, "缺少授权状态参数 (state)", "请返回 SubBar 重新点击「打开授权链接」。", "")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
		defer cancel()

		err := m.completeSession(ctx, state, code, errParam, errDesc)
		if err != nil {
			renderCallbackHTML(w, http.StatusBadRequest, false, "授权未完成", err.Error(), "")
			return
		}

		st, _ := m.Status(state)
		renderCallbackHTML(w, http.StatusOK, true, "授权成功 · 已自动同步至 SubBar", "凭证与自动续期令牌已安全写入本机并完成配额拉取，现在可直接关闭此页面。", st.AccountEmail)
	})
}

func (m *Manager) completeSession(ctx context.Context, state, code, oauthErr, oauthErrDesc string) error {
	m.mu.Lock()
	sess, ok := m.sessions[state]
	if !ok {
		m.mu.Unlock()
		return errors.New("授权会话不存在或已过期，请重新点击生成链接")
	}
	if sess.status == "completed" {
		m.mu.Unlock()
		return nil
	}
	if time.Now().UTC().After(sess.expiresAt) {
		sess.status = "expired"
		sess.errMsg = "授权会话已过期（超过 5 分钟），请重新生成链接"
		m.mu.Unlock()
		go m.gc()
		return errors.New(sess.errMsg)
	}
	if oauthErr != "" {
		msg := oauthErr
		if oauthErrDesc != "" {
			msg = oauthErr + ": " + oauthErrDesc
		}
		sess.status = "error"
		sess.errMsg = "浏览器授权被取消或拒绝 (" + msg + ")"
		m.mu.Unlock()
		go m.gc()
		return errors.New(sess.errMsg)
	}
	if strings.TrimSpace(code) == "" {
		m.mu.Unlock()
		return errors.New("回调中未包含有效的授权码 (code)")
	}
	if sess.status == "exchanging" {
		m.mu.Unlock()
		return nil
	}
	sess.status = "exchanging"
	reqCopy := sess.req
	verifier := sess.verifier
	redirectURI := sess.redirectURI
	m.mu.Unlock()

	upd, err := providers.ExchangeOAuthCode(ctx, reqCopy.Provider, code, state, verifier, redirectURI)
	if err != nil {
		m.mu.Lock()
		sess.status = "error"
		sess.errMsg = err.Error()
		m.mu.Unlock()
		go m.gc()
		return err
	}

	return m.finalizeAuthorizedSession(sess, reqCopy, upd)
}

func (m *Manager) finalizeAuthorizedSession(sess *session, reqCopy StartRequest, upd providers.TokenUpdate) error {
	saved, err := m.persistAuthorizedEntry(reqCopy, upd)
	if err != nil {
		m.mu.Lock()
		sess.status = "error"
		sess.errMsg = err.Error()
		m.mu.Unlock()
		go m.gc()
		return err
	}

	// Immediately fetch initial quota snapshot so UI shows live bars right away!
	pCtx, cancel := context.WithTimeout(context.Background(), 16*time.Second)
	snap, _ := m.poller.RefreshOne(pCtx, saved.ID)
	cancel()

	if refreshedCfg, ok := m.store.GetEntry(saved.ID); ok {
		saved = refreshedCfg
	}
	if saved.AccountEmail == "" && strings.TrimSpace(snap.AccountEmail) != "" {
		saved.AccountEmail = strings.TrimSpace(snap.AccountEmail)
		if strings.TrimSpace(reqCopy.Name) == "" && strings.TrimSpace(reqCopy.EntryID) == "" {
			saved.Name = defaultOAuthEntryName(reqCopy.Provider, saved.AccountEmail)
			snap.Name = saved.Name
		}
		if updatedCfg, uErr := m.store.UpdateEntry(saved.ID, saved, true); uErr == nil {
			saved = updatedCfg
			if mergedSnap, ok := m.store.GetSnapshot(saved.ID); ok {
				snap = mergedSnap
			}
		}
	}
	pub := saved.Public()

	m.mu.Lock()
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.status = "completed"
	sess.errMsg = ""
	sess.accountEmail = saved.AccountEmail
	sess.entry = &pub
	sess.snapshot = &snap
	m.mu.Unlock()

	// Release any ephemeral callback port after a short grace delay so browser response flushes cleanly.
	time.AfterFunc(1200*time.Millisecond, m.gc)
	return nil
}

func (m *Manager) persistAuthorizedEntry(req StartRequest, upd providers.TokenUpdate) (model.EntryConfig, error) {
	prov, ok := m.registry.Get(req.Provider)
	if !ok {
		return model.EntryConfig{}, fmt.Errorf("未知的 Provider: %s", req.Provider)
	}
	desc := prov.Descriptor()

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// Re-authorizing an existing entry (explicit entryId OR same provider + account already stored) updates it in-place.
	var matchedExisting *model.EntryConfig
	explicitReauth := false
	if strings.TrimSpace(req.EntryID) != "" {
		if existing, exists := m.store.GetEntry(strings.TrimSpace(req.EntryID)); exists {
			matchedExisting = &existing
			explicitReauth = true
		}
	}
	if matchedExisting == nil {
		probeCandidate := model.EntryConfig{
			Provider:     req.Provider,
			BaseURL:      strings.TrimSpace(req.BaseURL),
			AccountEmail: upd.AccountEmail,
			AccountID:    coalesceNonEmpty(req.AccountID, upd.AccountID),
			Secret:       upd.AccessToken,
			RefreshToken: upd.RefreshToken,
		}
		if dup, exists := m.store.FindDuplicate(probeCandidate); exists {
			matchedExisting = &dup
		}
	}
	if matchedExisting != nil {
		existing := *matchedExisting
		if n := strings.TrimSpace(req.Name); n != "" {
			existing.Name = n
		}
		if b := strings.TrimSpace(req.Badge); b != "" {
			existing.Badge = b
		}
		existing.Enabled = enabled
		if explicitReauth {
			existing.Pinned = req.Pinned
		} else {
			existing.Pinned = existing.Pinned || req.Pinned
		}
		if req.RefreshSeconds >= 30 {
			existing.RefreshSeconds = req.RefreshSeconds
		}
		if req.MonthlyCost != nil {
			existing.MonthlyCost = req.MonthlyCost
		}
		if req.CostCurrency != "" {
			existing.CostCurrency = req.CostCurrency
		}
		if req.DashboardURL != "" {
			existing.DashboardURL = req.DashboardURL
		}
		if req.Notes != "" {
			existing.Notes = req.Notes
		}
		if req.PlanLabel != "" {
			existing.PlanLabel = req.PlanLabel
		}
		if strings.TrimSpace(req.BaseURL) != "" {
			existing.BaseURL = strings.TrimSpace(req.BaseURL)
		}
		existing.AuthMode = "oauth"
		existing.Secret = upd.AccessToken
		if upd.RefreshToken != "" {
			existing.RefreshToken = upd.RefreshToken
		}
		existing.TokenExpiresAt = upd.TokenExpiresAt
		if upd.AccountEmail != "" {
			existing.AccountEmail = upd.AccountEmail
		}
		if upd.AccountID != "" {
			existing.AccountID = upd.AccountID
		} else if req.AccountID != "" {
			existing.AccountID = strings.TrimSpace(req.AccountID)
		}
		return m.store.UpdateEntry(existing.ID, existing, true)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultOAuthEntryName(req.Provider, upd.AccountEmail)
	}
	badge := strings.TrimSpace(req.Badge)
	if badge == "" {
		badge = desc.DefaultBadge
	}
	refreshSec := req.RefreshSeconds
	if refreshSec < 30 {
		refreshSec = desc.DefaultRefreshSeconds
		if refreshSec < 30 {
			refreshSec = 300
		}
	}
	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		accountID = upd.AccountID
	}

	cfg := model.EntryConfig{
		Name:           name,
		Provider:       req.Provider,
		Kind:           model.KindSubscription,
		Badge:          badge,
		Enabled:        enabled,
		Pinned:         req.Pinned,
		RefreshSeconds: refreshSec,
		DashboardURL:   strings.TrimSpace(req.DashboardURL),
		Notes:          strings.TrimSpace(req.Notes),
		MonthlyCost:    req.MonthlyCost,
		CostCurrency:   strings.TrimSpace(req.CostCurrency),
		PlanLabel:      strings.TrimSpace(req.PlanLabel),
		BaseURL:        strings.TrimSpace(req.BaseURL),
		AuthMode:       "oauth",
		Secret:         upd.AccessToken,
		RefreshToken:   upd.RefreshToken,
		TokenExpiresAt: upd.TokenExpiresAt,
		AccountEmail:   upd.AccountEmail,
		AccountID:      accountID,
	}
	return m.store.AddEntry(cfg)
}

func (m *Manager) ensurePortListenerLocked(port int) error {
	if _, running := m.listeners[port]; running {
		return nil
	}

	handler := m.CallbackHandler()
	var servers []*http.Server

	// Bind IPv4 loopback first (required), and IPv6 loopback best-effort so macOS "localhost" resolves with zero delay.
	ln4, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	srv4 := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 8 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      25 * time.Second,
	}
	servers = append(servers, srv4)
	go func() { _ = srv4.Serve(ln4) }()

	if ln6, err6 := net.Listen("tcp6", "[::1]:"+strconv.Itoa(port)); err6 == nil {
		srv6 := &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 8 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      25 * time.Second,
		}
		servers = append(servers, srv6)
		go func() { _ = srv6.Serve(ln6) }()
	}

	m.listeners[port] = &portServer{
		port:    port,
		servers: servers,
	}
	return nil
}

func (m *Manager) gc() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	neededPorts := make(map[int]bool)

	for st, sess := range m.sessions {
		if sess.status == "pending" && now.After(sess.expiresAt) {
			if sess.cancel != nil {
				sess.cancel()
			}
			sess.status = "expired"
			sess.errMsg = "等待授权超时"
		}
		// Keep completed/error session statuses queryable for 3 minutes so polling clients never miss final state.
		if now.After(sess.expiresAt.Add(3 * time.Minute)) {
			if sess.cancel != nil {
				sess.cancel()
			}
			delete(m.sessions, st)
			continue
		}
		if (sess.status == "pending" || sess.status == "exchanging") && sess.port > 0 {
			neededPorts[sess.port] = true
		}
	}

	for port, ps := range m.listeners {
		if !neededPorts[port] {
			ps.Close()
			delete(m.listeners, port)
		}
	}
}

func coalesceNonEmpty(a, b string) string {
	if s := strings.TrimSpace(a); s != "" {
		return s
	}
	return strings.TrimSpace(b)
}

func defaultOAuthEntryName(providerID, email string) string {
	base := "订阅账号"
	switch providerID {
	case "antigravity_oauth":
		base = "Antigravity"
	case "codex_oauth":
		base = "Codex"
	case "claude_oauth":
		base = "Claude"
	case "kimi_oauth":
		base = "Kimi 订阅"
	}
	shortUser := strings.TrimSpace(email)
	if idx := strings.IndexByte(shortUser, '@'); idx > 0 {
		shortUser = shortUser[:idx]
	}
	if shortUser != "" {
		return fmt.Sprintf("%s (%s)", base, shortUser)
	}
	return base
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func generatePKCE() (verifier string, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func renderCallbackHTML(w http.ResponseWriter, status int, ok bool, title, detail, email string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	accent := "#22c55e"
	icon := "✓"
	if !ok {
		accent = "#f87171"
		icon = "✕"
	}
	emailHTML := ""
	if email != "" {
		emailHTML = fmt.Sprintf(`<div class="pill">%s</div>`, html.EscapeString(email))
	}
	autoCloseJS := ""
	if ok {
		autoCloseJS = `<script>setTimeout(function(){try{window.close();}catch(e){}},1500);</script>`
	}

	page := fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; background:#05070a; color:#ffffff; font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text","PingFang SC",sans-serif; padding:20px; }
  .box { max-width:440px; width:100%%; background:#0e131b; border:2px solid %s; border-radius:16px; padding:28px 24px; text-align:center; box-shadow:0 12px 36px rgba(0,0,0,0.7); }
  .mark { width:54px; height:54px; border-radius:999px; background:%s; color:#041108; font-size:28px; font-weight:900; display:inline-flex; align-items:center; justify-content:center; margin-bottom:14px; }
  h1 { margin:0 0 10px; font-size:20px; font-weight:800; }
  .pill { display:inline-block; margin-bottom:12px; padding:4px 12px; border-radius:999px; background:#151d29; border:1.5px solid #475569; font-family:ui-monospace,Menlo,monospace; font-size:13px; font-weight:700; color:#2dd4bf; }
  p { margin:0; font-size:14.5px; line-height:1.55; color:#e2e8f0; }
</style>
</head>
<body>
  <div class="box">
    <div class="mark">%s</div>
    <h1>%s</h1>
    %s
    <p>%s</p>
  </div>
  %s
</body>
</html>`,
		html.EscapeString(title),
		accent,
		accent,
		icon,
		html.EscapeString(title),
		emailHTML,
		html.EscapeString(detail),
		autoCloseJS,
	)
	_, _ = w.Write([]byte(page))
}
