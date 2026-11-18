package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Public OAuth client identifiers used by official CLI/desktop integrations are built in
// as out-of-the-box defaults, and can be overridden at runtime (in priority order):
//
//  1. environment variables SUBBAR_OAUTH_<PROVIDER>_CLIENT_ID / _CLIENT_SECRET
//     (e.g. SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID)
//  2. a JSON file loaded via LoadOAuthClients (default: <data-dir>/oauth-clients.json):
//     {"antigravity_oauth": {"client_id": "...", "client_secret": "..."}, ...}
//  3. built-in public client defaults (defaultOAuthClients)
type OAuthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

var defaultOAuthClients = map[string]OAuthClient{
	"antigravity_oauth": {
		ClientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
		ClientSecret: "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",
	},
	"codex_oauth": {
		ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
	},
	"claude_oauth": {
		ClientID: "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
	},
	"kimi_oauth": {
		ClientID: "17e5f671-d194-4dfb-9706-5516cb48c098",
	},
}

var (
	oauthClientsMu sync.RWMutex
	oauthClients   = map[string]OAuthClient{}
)

// SetOAuthClient stores credentials in memory and persists them (0600, atomic) to path.
// Empty fields keep the previously stored value, so a secret need not be re-entered.
func SetOAuthClient(path, providerID, id, secret string) error {
	if _, ok := oauthSpecs[providerID]; !ok {
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}
	oauthClientsMu.Lock()
	defer oauthClientsMu.Unlock()
	next := make(map[string]OAuthClient, len(oauthClients)+1)
	for k, v := range oauthClients {
		next[k] = v
	}
	c := next[providerID]
	if v := strings.TrimSpace(id); v != "" {
		c.ClientID = v
	}
	if v := strings.TrimSpace(secret); v != "" {
		c.ClientSecret = v
	}
	next[providerID] = c
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	oauthClients = next
	return nil
}

// OAuthClientStatus describes configuration state without revealing secrets.
type OAuthClientStatus struct {
	Provider     string `json:"provider"`
	Configured   bool   `json:"configured"`
	NeedsSecret  bool   `json:"needsSecret"`
	HasSecret    bool   `json:"hasSecret"`
	ClientIDHint string `json:"clientIdHint,omitempty"`
}

func OAuthClientStatusFor(providerID string) (OAuthClientStatus, bool) {
	spec, ok := oauthSpecs[providerID]
	if !ok {
		return OAuthClientStatus{}, false
	}
	id, secret := OAuthClientFor(providerID)
	st := OAuthClientStatus{Provider: providerID, NeedsSecret: spec.NeedsSecret, HasSecret: secret != ""}
	st.Configured = id != "" && (!spec.NeedsSecret || secret != "")
	if len(id) > 8 {
		st.ClientIDHint = id[:4] + "••••" + id[len(id)-4:]
	} else if id != "" {
		st.ClientIDHint = "••••"
	}
	return st, true
}

// LoadOAuthClients reads the JSON credentials file. A missing file is not an error.
func LoadOAuthClients(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	m := map[string]OAuthClient{}
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	oauthClientsMu.Lock()
	oauthClients = m
	oauthClientsMu.Unlock()
	return nil
}

// OAuthClientFor returns the effective client credentials for a provider
// (priority: env vars > oauth-clients.json > built-in defaults).
func OAuthClientFor(providerID string) (id, secret string) {
	def := defaultOAuthClients[providerID]
	id = def.ClientID
	secret = def.ClientSecret

	oauthClientsMu.RLock()
	c := oauthClients[providerID]
	oauthClientsMu.RUnlock()
	if v := strings.TrimSpace(c.ClientID); v != "" {
		id = v
	}
	if v := strings.TrimSpace(c.ClientSecret); v != "" {
		secret = v
	}

	key := "SUBBAR_OAUTH_" + strings.ToUpper(providerID)
	if v := strings.TrimSpace(os.Getenv(key + "_CLIENT_ID")); v != "" {
		id = v
	}
	if v := strings.TrimSpace(os.Getenv(key + "_CLIENT_SECRET")); v != "" {
		secret = v
	}
	return id, secret
}
