package providers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/letrahoo/sub-bar/server/internal/model"
)

type OAuthSpec struct {
	ProviderID        string
	DisplayName       string
	ClientID          string
	ClientSecret      string
	NeedsSecret       bool
	AuthURL           string
	TokenURL          string
	CallbackPort      int
	CallbackPath      string
	Scopes            []string
	ExtraAuthParams   map[string]string
	JSONTokenExchange bool
	DeviceFlow        bool
}

const (
	kimiCLIVersion   = "1.12.0"
	kimiCLIUserAgent = "KimiCLI/1.12.0"
)

var (
	ErrDeviceAuthPending  = errors.New("authorization_pending")
	ErrDeviceAuthSlowDown = errors.New("slow_down")
)

type DeviceAuthSession struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
	AuthBaseURL             string
	DeviceID                string
}

var oauthSpecs = map[string]OAuthSpec{
	"antigravity_oauth": {
		ProviderID:   "antigravity_oauth",
		DisplayName:  "Antigravity (agy)",
		NeedsSecret:  true,
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		CallbackPort: 51121,
		CallbackPath: "/oauth-callback",
		Scopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/aicode",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
			"https://www.googleapis.com/auth/cclog",
			"https://www.googleapis.com/auth/experimentsandconfigs",
		},
		ExtraAuthParams: map[string]string{
			"access_type": "offline",
			"prompt":      "consent",
		},
	},
	"codex_oauth": {
		ProviderID:   "codex_oauth",
		DisplayName:  "OpenAI Codex / ChatGPT",
		AuthURL:      "https://auth.openai.com/oauth/authorize",
		TokenURL:     "https://auth.openai.com/oauth/token",
		CallbackPort: 1455,
		CallbackPath: "/auth/callback",
		Scopes: []string{
			"openid",
			"profile",
			"email",
			"offline_access",
		},
		ExtraAuthParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
		},
	},
	"claude_oauth": {
		ProviderID:        "claude_oauth",
		DisplayName:       "Claude Pro / Max",
		AuthURL:           "https://claude.ai/oauth/authorize",
		TokenURL:          "https://console.anthropic.com/v1/oauth/token",
		CallbackPort:      54545,
		CallbackPath:      "/callback",
		Scopes:            []string{"org:create_api_key", "user:profile", "user:inference"},
		ExtraAuthParams:   map[string]string{"code": "true"},
		JSONTokenExchange: true,
	},
	"kimi_oauth": {
		ProviderID:  "kimi_oauth",
		DisplayName: "Kimi 会员 / Kimi Code",
		AuthURL:     "https://auth.kimi.com/api/oauth/device_authorization",
		TokenURL:    "https://auth.kimi.com/api/oauth/token",
		DeviceFlow:  true,
	},
}

func GetOAuthSpec(providerID string) (OAuthSpec, bool) {
	spec, ok := oauthSpecs[providerID]
	if !ok {
		return spec, false
	}
	spec.ClientID, spec.ClientSecret = OAuthClientFor(providerID)
	return spec, true
}

func (s OAuthSpec) DefaultRedirectURI() string {
	return fmt.Sprintf("http://localhost:%d%s", s.CallbackPort, s.CallbackPath)
}

func BuildAuthURL(spec OAuthSpec, state, codeChallenge, redirectURI string) string {
	if redirectURI == "" {
		redirectURI = spec.DefaultRedirectURI()
	}
	q := url.Values{}
	q.Set("client_id", spec.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	if len(spec.Scopes) > 0 {
		q.Set("scope", strings.Join(spec.Scopes, " "))
	}
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	for k, v := range spec.ExtraAuthParams {
		q.Set(k, v)
	}
	return spec.AuthURL + "?" + q.Encode()
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Account      *struct {
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func ExchangeOAuthCode(ctx context.Context, providerID, rawCode, state, verifier, redirectURI string) (TokenUpdate, error) {
	spec, ok := GetOAuthSpec(providerID)
	if !ok {
		return TokenUpdate{}, fmt.Errorf("不支持 OAuth 授权的服务类型: %s", providerID)
	}
	if redirectURI == "" {
		redirectURI = spec.DefaultRedirectURI()
	}

	// Anthropic's manual code page sometimes concatenates "code#state"
	code := strings.TrimSpace(rawCode)
	if idx := strings.IndexByte(code, '#'); idx > 0 {
		code = code[:idx]
	}
	if code == "" {
		return TokenUpdate{}, errors.New("缺少授权码 code")
	}

	var body []byte
	var status int
	var err error

	if spec.JSONTokenExchange {
		payload := map[string]string{
			"grant_type":    "authorization_code",
			"code":          code,
			"state":         state,
			"client_id":     spec.ClientID,
			"redirect_uri":  redirectURI,
			"code_verifier": verifier,
		}
		body, status, err = doJSONBodyRequest(ctx, http.MethodPost, spec.TokenURL, map[string]string{
			"User-Agent": "claude-code/2.1.0",
		}, payload)
	} else {
		form := url.Values{}
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("client_id", spec.ClientID)
		if spec.ClientSecret != "" {
			form.Set("client_secret", spec.ClientSecret)
		}
		form.Set("redirect_uri", redirectURI)
		form.Set("code_verifier", verifier)
		body, status, err = doRawRequest(ctx, http.MethodPost, spec.TokenURL, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		}, []byte(form.Encode()))
	}
	if err != nil {
		return TokenUpdate{}, err
	}

	var tok oauthTokenResponse
	_ = json.Unmarshal(body, &tok)
	if status < 200 || status >= 300 || strings.TrimSpace(tok.AccessToken) == "" {
		msg := coalesceStr(tok.ErrorDescription, tok.Error)
		if msg == "" {
			msg = truncateErrBody(body)
		}
		return TokenUpdate{}, fmt.Errorf("换取 OAuth 令牌失败 (HTTP %d): %s", status, msg)
	}

	upd := buildTokenUpdateFromResponse(ctx, providerID, tok)
	return upd, nil
}

func kimiAuthBaseURL(baseURLHint string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(baseURLHint)), "kimi.ai") {
		return "https://auth.kimi.ai"
	}
	return "https://auth.kimi.com"
}

func newKimiDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "subbar-mac-device-0001"
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func kimiClientHeaders(deviceID string) map[string]string {
	did := strings.TrimSpace(deviceID)
	if did == "" {
		did = "subbar-kimi-default-device"
	}
	return map[string]string{
		"User-Agent":         kimiCLIUserAgent,
		"X-Msh-Platform":     "kimi_cli",
		"X-Msh-Version":      kimiCLIVersion,
		"X-Msh-Device-Name":  "SubBar",
		"X-Msh-Device-Model": runtime.GOARCH,
		"X-Msh-Os-Version":   runtime.GOOS,
		"X-Msh-Device-Id":    did,
	}
}

func StartDeviceOAuth(ctx context.Context, providerID, baseURLHint string) (DeviceAuthSession, error) {
	spec, ok := GetOAuthSpec(providerID)
	if !ok || !spec.DeviceFlow {
		return DeviceAuthSession{}, fmt.Errorf("该服务不支持设备码授权: %s", providerID)
	}
	authBase := kimiAuthBaseURL(baseURLHint)
	deviceID := newKimiDeviceID()

	form := url.Values{}
	form.Set("client_id", spec.ClientID)
	headers := kimiClientHeaders(deviceID)
	headers["Content-Type"] = "application/x-www-form-urlencoded"

	body, status, err := doRawRequest(ctx, http.MethodPost, authBase+"/api/oauth/device_authorization", headers, []byte(form.Encode()))
	if err != nil {
		return DeviceAuthSession{}, err
	}
	var resp struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &resp)
	if status < 200 || status >= 300 || strings.TrimSpace(resp.DeviceCode) == "" {
		msg := coalesceStr(resp.ErrorDescription, resp.Error)
		if msg == "" {
			msg = truncateErrBody(body)
		}
		return DeviceAuthSession{}, fmt.Errorf("创建 Kimi 授权会话失败 (HTTP %d): %s", status, msg)
	}

	authLink := strings.TrimSpace(resp.VerificationURIComplete)
	if authLink == "" && strings.TrimSpace(resp.VerificationURI) != "" {
		authLink = strings.TrimSpace(resp.VerificationURI)
		if strings.TrimSpace(resp.UserCode) != "" {
			sep := "?"
			if strings.Contains(authLink, "?") {
				sep = "&"
			}
			authLink += sep + "user_code=" + url.QueryEscape(strings.TrimSpace(resp.UserCode))
		}
	}
	if authLink == "" {
		return DeviceAuthSession{}, errors.New("上游未返回有效的授权跳转链接")
	}

	return DeviceAuthSession{
		DeviceCode:              strings.TrimSpace(resp.DeviceCode),
		UserCode:                strings.TrimSpace(resp.UserCode),
		VerificationURI:         strings.TrimSpace(resp.VerificationURI),
		VerificationURIComplete: authLink,
		ExpiresIn:               resp.ExpiresIn,
		Interval:                resp.Interval,
		AuthBaseURL:             authBase,
		DeviceID:                deviceID,
	}, nil
}

func PollDeviceOAuthToken(ctx context.Context, providerID, authBaseURL, deviceCode, deviceID string) (TokenUpdate, error) {
	spec, ok := GetOAuthSpec(providerID)
	if !ok || !spec.DeviceFlow {
		return TokenUpdate{}, fmt.Errorf("不支持设备码轮询的服务: %s", providerID)
	}
	authBase := strings.TrimRight(strings.TrimSpace(authBaseURL), "/")
	if authBase == "" {
		authBase = "https://auth.kimi.com"
	}

	form := url.Values{}
	form.Set("client_id", spec.ClientID)
	form.Set("device_code", strings.TrimSpace(deviceCode))
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	headers := kimiClientHeaders(deviceID)
	headers["Content-Type"] = "application/x-www-form-urlencoded"

	body, status, err := doRawRequest(ctx, http.MethodPost, authBase+"/api/oauth/token", headers, []byte(form.Encode()))
	if err != nil {
		return TokenUpdate{}, err
	}

	var tok oauthTokenResponse
	_ = json.Unmarshal(body, &tok)
	if status >= 200 && status < 300 && strings.TrimSpace(tok.AccessToken) != "" {
		upd := buildTokenUpdateFromResponse(ctx, providerID, tok)
		if upd.AccountID == "" && strings.TrimSpace(deviceID) != "" {
			upd.AccountID = strings.TrimSpace(deviceID)
		}
		return upd, nil
	}

	switch strings.TrimSpace(tok.Error) {
	case "authorization_pending":
		return TokenUpdate{}, ErrDeviceAuthPending
	case "slow_down":
		return TokenUpdate{}, ErrDeviceAuthSlowDown
	case "expired_token":
		return TokenUpdate{}, errors.New("Kimi 授权码已过期，请重新点击生成链接")
	case "access_denied":
		return TokenUpdate{}, errors.New("已在网页端取消或拒绝 Kimi 授权")
	default:
		msg := coalesceStr(tok.ErrorDescription, tok.Error)
		if msg == "" {
			msg = truncateErrBody(body)
		}
		return TokenUpdate{}, fmt.Errorf("Kimi 授权轮询失败 (HTTP %d): %s", status, msg)
	}
}

func RefreshKimiOAuthToken(ctx context.Context, refreshToken, baseURLHint, deviceID string) (TokenUpdate, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return TokenUpdate{}, errors.New("缺少 Kimi refresh_token，请重新点击链接扫码授权")
	}
	authBase := kimiAuthBaseURL(baseURLHint)
	form := url.Values{}
	kimiID, _ := OAuthClientFor("kimi_oauth")
	form.Set("client_id", kimiID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)

	headers := kimiClientHeaders(deviceID)
	headers["Content-Type"] = "application/x-www-form-urlencoded"

	body, status, err := doRawRequest(ctx, http.MethodPost, authBase+"/api/oauth/token", headers, []byte(form.Encode()))
	if err != nil {
		return TokenUpdate{}, err
	}
	var tok oauthTokenResponse
	_ = json.Unmarshal(body, &tok)
	if status < 200 || status >= 300 || strings.TrimSpace(tok.AccessToken) == "" {
		msg := coalesceStr(tok.ErrorDescription, tok.Error)
		if msg == "" {
			msg = truncateErrBody(body)
		}
		return TokenUpdate{}, fmt.Errorf("Kimi 自动续期令牌失败 (HTTP %d): %s", status, msg)
	}
	upd := buildTokenUpdateFromResponse(ctx, "kimi_oauth", tok)
	if upd.RefreshToken == "" {
		upd.RefreshToken = rt
	}
	if upd.AccountID == "" && strings.TrimSpace(deviceID) != "" {
		upd.AccountID = strings.TrimSpace(deviceID)
	}
	return upd, nil
}

func RefreshOAuthToken(ctx context.Context, providerID, refreshToken string) (TokenUpdate, error) {
	if providerID == "kimi_oauth" {
		return RefreshKimiOAuthToken(ctx, refreshToken, "", "")
	}
	spec, ok := GetOAuthSpec(providerID)
	if !ok {
		return TokenUpdate{}, fmt.Errorf("不支持刷新令牌的服务: %s", providerID)
	}
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return TokenUpdate{}, errors.New("缺少 refresh_token，请重新点击链接登录授权")
	}

	var body []byte
	var status int
	var err error

	if spec.JSONTokenExchange {
		payload := map[string]string{
			"grant_type":    "refresh_token",
			"refresh_token": rt,
			"client_id":     spec.ClientID,
		}
		body, status, err = doJSONBodyRequest(ctx, http.MethodPost, spec.TokenURL, map[string]string{
			"User-Agent": "claude-code/2.1.0",
		}, payload)
	} else {
		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", rt)
		form.Set("client_id", spec.ClientID)
		if spec.ClientSecret != "" {
			form.Set("client_secret", spec.ClientSecret)
		}
		body, status, err = doRawRequest(ctx, http.MethodPost, spec.TokenURL, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		}, []byte(form.Encode()))
	}
	if err != nil {
		return TokenUpdate{}, err
	}

	var tok oauthTokenResponse
	_ = json.Unmarshal(body, &tok)
	if status < 200 || status >= 300 || strings.TrimSpace(tok.AccessToken) == "" {
		msg := coalesceStr(tok.ErrorDescription, tok.Error)
		if msg == "" {
			msg = truncateErrBody(body)
		}
		return TokenUpdate{}, fmt.Errorf("自动续期令牌失败 (HTTP %d): %s", status, msg)
	}

	upd := buildTokenUpdateFromResponse(ctx, providerID, tok)
	if upd.RefreshToken == "" {
		upd.RefreshToken = rt
	}
	return upd, nil
}

func buildTokenUpdateFromResponse(ctx context.Context, providerID string, tok oauthTokenResponse) TokenUpdate {
	var exp *time.Time
	if tok.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
		exp = &t
	}
	email, accountID := extractJWTIdentity(tok.IDToken)
	if email == "" || accountID == "" {
		e2, a2 := extractJWTIdentity(tok.AccessToken)
		if email == "" {
			email = e2
		}
		if accountID == "" {
			accountID = a2
		}
	}
	if email == "" && tok.Account != nil && tok.Account.EmailAddress != "" {
		email = strings.TrimSpace(tok.Account.EmailAddress)
	}
	if email == "" && providerID == "antigravity_oauth" {
		email = fetchGoogleUserEmail(ctx, tok.AccessToken)
	}

	return TokenUpdate{
		AccessToken:    strings.TrimSpace(tok.AccessToken),
		RefreshToken:   strings.TrimSpace(tok.RefreshToken),
		TokenExpiresAt: exp,
		AccountEmail:   email,
		AccountID:      accountID,
	}
}

func fetchGoogleUserEmail(ctx context.Context, accessToken string) string {
	uCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	body, status, err := doJSONRequest(uCtx, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", map[string]string{
		"Authorization": "Bearer " + accessToken,
	})
	if err != nil || status != http.StatusOK {
		return ""
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err == nil {
		return strings.TrimSpace(info.Email)
	}
	return ""
}

func extractJWTIdentity(rawJWT string) (email string, accountID string) {
	parts := strings.Split(strings.TrimSpace(rawJWT), ".")
	if len(parts) < 2 {
		return "", ""
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return "", ""
	}
	if em, ok := claims["email"].(string); ok {
		email = strings.TrimSpace(em)
	}
	if prof, ok := claims["https://api.openai.com/profile"].(map[string]any); ok && email == "" {
		if em, ok := prof["email"].(string); ok {
			email = strings.TrimSpace(em)
		}
	}
	if authObj, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if aid, ok := authObj["chatgpt_account_id"].(string); ok {
			accountID = strings.TrimSpace(aid)
		}
	}
	return email, accountID
}

func tokenNeedsProactiveRefresh(cfg model.EntryConfig) bool {
	if strings.TrimSpace(cfg.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return true
	}
	if cfg.TokenExpiresAt != nil && time.Until(*cfg.TokenExpiresAt) < 2*time.Minute {
		return true
	}
	return false
}

// ImportLocalDiscovery imports an already-authenticated local CLI session into SubBar's managed OAuth store
// without exposing any file paths to the UI.
func ImportLocalDiscovery(ctx context.Context, hintID string) (model.EntryConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return model.EntryConfig{}, errors.New("无法定位用户目录")
	}

	switch hintID {
	case "codex_local":
		path := filepath.Join(home, ".codex", "auth.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			return model.EntryConfig{}, fmt.Errorf("未找到 Codex 登录态: %w", err)
		}
		var doc struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			AccountID    string `json:"account_id"`
			Tokens       *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				IDToken      string `json:"id_token"`
				AccountID    string `json:"account_id"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return model.EntryConfig{}, fmt.Errorf("解析 Codex 凭据失败: %w", err)
		}
		at := doc.AccessToken
		rt := doc.RefreshToken
		idt := doc.IDToken
		aid := doc.AccountID
		if doc.Tokens != nil && (doc.Tokens.AccessToken != "" || doc.Tokens.RefreshToken != "") {
			at = doc.Tokens.AccessToken
			rt = doc.Tokens.RefreshToken
			idt = doc.Tokens.IDToken
			aid = coalesceStr(doc.Tokens.AccountID, aid)
		}
		if strings.TrimSpace(at) == "" && strings.TrimSpace(rt) == "" {
			return model.EntryConfig{}, errors.New("Codex 凭据为空，请使用链接授权登录")
		}
		email, jwtAID := extractJWTIdentity(idt)
		if email == "" {
			e2, a2 := extractJWTIdentity(at)
			email = e2
			if jwtAID == "" {
				jwtAID = a2
			}
		}
		if aid == "" {
			aid = jwtAID
		}
		return model.EntryConfig{
			Name:         "OpenAI Codex",
			Provider:     "codex_oauth",
			Kind:         model.KindSubscription,
			Badge:        "CDX",
			Enabled:      true,
			AuthMode:     "oauth",
			Secret:       strings.TrimSpace(at),
			RefreshToken: strings.TrimSpace(rt),
			AccountID:    strings.TrimSpace(aid),
			AccountEmail: email,
		}, nil

	case "claude_local":
		var raw []byte
		filePath := filepath.Join(home, ".claude", ".credentials.json")
		if b, err := os.ReadFile(filePath); err == nil && len(b) > 0 {
			raw = b
		} else if runtime.GOOS == "darwin" {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			out, err := exec.CommandContext(cctx, "/usr/bin/security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
			if err != nil {
				return model.EntryConfig{}, errors.New("未读取到本机 Claude 登录态，请使用链接授权登录")
			}
			raw = out
		} else {
			return model.EntryConfig{}, errors.New("未找到本机 Claude 登录态，请使用链接授权登录")
		}

		var doc struct {
			AccessToken   string `json:"access_token"`
			RefreshToken  string `json:"refresh_token"`
			ClaudeAiOauth *struct {
				AccessToken      string `json:"accessToken"`
				RefreshToken     string `json:"refreshToken"`
				ExpiresAt        int64  `json:"expiresAt"`
				SubscriptionType string `json:"subscriptionType"`
			} `json:"claudeAiOauth"`
		}
		trimmed := strings.TrimSpace(string(raw))
		if strings.HasPrefix(trimmed, "sk-ant-") {
			return model.EntryConfig{
				Name:     "Claude Code",
				Provider: "claude_oauth",
				Kind:     model.KindSubscription,
				Badge:    "CLD",
				Enabled:  true,
				AuthMode: "oauth",
				Secret:   trimmed,
			}, nil
		}
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return model.EntryConfig{}, fmt.Errorf("解析 Claude 凭据失败: %w", err)
		}
		at := doc.AccessToken
		rt := doc.RefreshToken
		plan := ""
		var exp *time.Time
		if doc.ClaudeAiOauth != nil && doc.ClaudeAiOauth.AccessToken != "" {
			at = doc.ClaudeAiOauth.AccessToken
			rt = doc.ClaudeAiOauth.RefreshToken
			plan = strings.ToUpper(strings.TrimSpace(doc.ClaudeAiOauth.SubscriptionType))
			if doc.ClaudeAiOauth.ExpiresAt > 0 {
				exp = unixToTime(doc.ClaudeAiOauth.ExpiresAt)
			}
		}
		if strings.TrimSpace(at) == "" && strings.TrimSpace(rt) == "" {
			return model.EntryConfig{}, errors.New("Claude 凭据为空，请使用链接授权登录")
		}
		return model.EntryConfig{
			Name:           "Claude Code",
			Provider:       "claude_oauth",
			Kind:           model.KindSubscription,
			Badge:          "CLD",
			Enabled:        true,
			AuthMode:       "oauth",
			Secret:         strings.TrimSpace(at),
			RefreshToken:   strings.TrimSpace(rt),
			TokenExpiresAt: exp,
			PlanLabel:      plan,
		}, nil

	case "kimi_local":
		path := filepath.Join(home, ".kimi", "credentials", "kimi-code.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			return model.EntryConfig{}, fmt.Errorf("未找到 Kimi CLI 登录态: %w", err)
		}
		var doc struct {
			AccessToken  string  `json:"access_token"`
			RefreshToken string  `json:"refresh_token"`
			ExpiresAt    float64 `json:"expires_at"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return model.EntryConfig{}, fmt.Errorf("解析 Kimi 凭据失败: %w", err)
		}
		if strings.TrimSpace(doc.AccessToken) == "" && strings.TrimSpace(doc.RefreshToken) == "" {
			return model.EntryConfig{}, errors.New("Kimi 凭据为空，请使用微信扫码/网页链接授权")
		}
		var exp *time.Time
		if doc.ExpiresAt > 0 {
			exp = unixToTime(int64(doc.ExpiresAt))
		}
		deviceID := ""
		if b, err := os.ReadFile(filepath.Join(home, ".kimi", "device_id")); err == nil {
			deviceID = strings.TrimSpace(string(b))
		}
		return model.EntryConfig{
			Name:           "Kimi 订阅",
			Provider:       "kimi_oauth",
			Kind:           model.KindSubscription,
			Badge:          "KIMI",
			Enabled:        true,
			AuthMode:       "oauth",
			Secret:         strings.TrimSpace(doc.AccessToken),
			RefreshToken:   strings.TrimSpace(doc.RefreshToken),
			TokenExpiresAt: exp,
			AccountID:      deviceID,
		}, nil

	default:
		return model.EntryConfig{}, fmt.Errorf("未知的本机发现标识: %s", hintID)
	}
}
