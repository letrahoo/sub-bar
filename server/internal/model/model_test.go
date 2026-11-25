package model

import "testing"

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc", "••••"},
		{"123456", "••••"},
		{"sk-1234567890", "sk-••••7890"},
		{"AIzaSyDemoKey9999", "AIz••••9999"},
	}
	for _, tc := range cases {
		if got := MaskSecret(tc.in); got != tc.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEntryConfigPublicRedactsSecrets(t *testing.T) {
	cfg := EntryConfig{
		ID:           "e-1",
		Name:         "Gemini",
		Provider:     "gemini_api",
		Secret:       "AIzaSySecretValue1234",
		RefreshToken: "1//refresh-token-secret",
	}
	pub := cfg.Public()
	if pub.Secret != "" || pub.RefreshToken != "" {
		t.Fatalf("Public() must clear Secret and RefreshToken, got secret=%q rt=%q", pub.Secret, pub.RefreshToken)
	}
	if !pub.HasSecret || !pub.HasRefreshToken {
		t.Fatalf("expected HasSecret and HasRefreshToken true, got %+v", pub)
	}
	if pub.SecretHint != "AIz••••1234" {
		t.Fatalf("unexpected SecretHint: %q", pub.SecretHint)
	}

	fileCfg := EntryConfig{AuthMode: "file"}
	if !fileCfg.Public().HasSecret {
		t.Fatalf("file authMode should report HasSecret=true")
	}
}

func TestEntrySnapshotHasMetrics(t *testing.T) {
	if (EntrySnapshot{}).HasMetrics() {
		t.Fatalf("empty snapshot must not report HasMetrics")
	}
	if !(EntrySnapshot{Windows: []QuotaWindow{{Key: "5h"}}}).HasMetrics() {
		t.Fatalf("snapshot with windows must report HasMetrics")
	}
	if !(EntrySnapshot{Balance: &BalanceInfo{Currency: "USD"}}).HasMetrics() {
		t.Fatalf("snapshot with balance must report HasMetrics")
	}
	if !(EntrySnapshot{Details: []KVDetail{{Label: "可用模型", Value: "38 个"}}}).HasMetrics() {
		t.Fatalf("snapshot with details must report HasMetrics")
	}
}

func TestCurrencySymbol(t *testing.T) {
	cases := map[string]string{
		"CNY": "¥",
		"RMB": "¥",
		"USD": "$",
		"EUR": "€",
		"HKD": "HK$",
		"JPY": "JP¥",
		"":    "$",
		"GBP": "GBP ",
	}
	for code, want := range cases {
		if got := CurrencySymbol(code); got != want {
			t.Errorf("CurrencySymbol(%q) = %q, want %q", code, got, want)
		}
	}
}
