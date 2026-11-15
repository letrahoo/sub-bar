package providers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOAuthClientsAreUserSuppliedAndPersisted(t *testing.T) {
	if id, _ := OAuthClientFor("antigravity_oauth"); id != "" {
		t.Fatalf("no credentials should be built in, got %q", id)
	}
	path := filepath.Join(t.TempDir(), "oauth-clients.json")
	if err := SetOAuthClient(path, "antigravity_oauth", "my-id-12345678", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := OAuthClientStatusFor("antigravity_oauth"); st.Configured {
		t.Fatal("secret is required for antigravity")
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
		t.Fatal("reload failed")
	}
	oauthClients = map[string]OAuthClient{}
}
