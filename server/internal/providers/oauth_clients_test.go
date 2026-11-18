package providers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOAuthClientsDefaultsAndOverrides(t *testing.T) {
	oauthClients = map[string]OAuthClient{}
	for _, providerID := range []string{"antigravity_oauth", "codex_oauth", "claude_oauth", "kimi_oauth"} {
		st, ok := OAuthClientStatusFor(providerID)
		if !ok || !st.Configured {
			t.Fatalf("expected built-in default for %s to be configured, got %+v", providerID, st)
		}
	}

	path := filepath.Join(t.TempDir(), "oauth-clients.json")
	if err := SetOAuthClient(path, "antigravity_oauth", "my-id-12345678", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetOAuthClient(path, "antigravity_oauth", "", "my-secret"); err != nil {
		t.Fatal(err)
	}
	st, _ := OAuthClientStatusFor("antigravity_oauth")
	if !st.Configured || !st.HasSecret || st.ClientIDHint == "my-id-12345678" {
		t.Fatalf("unexpected status %+v", st)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	oauthClients = map[string]OAuthClient{}
	if err := LoadOAuthClients(path); err != nil {
		t.Fatal(err)
	}
	if id, sec := OAuthClientFor("antigravity_oauth"); id != "my-id-12345678" || sec != "my-secret" {
		t.Fatalf("reload override failed: got %q / %q", id, sec)
	}

	t.Setenv("SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID", "env-override-id")
	if id, sec := OAuthClientFor("antigravity_oauth"); id != "env-override-id" || sec != "my-secret" {
		t.Fatalf("env override failed: got %q / %q", id, sec)
	}
	oauthClients = map[string]OAuthClient{}
}
