package serverstore

import "testing"

func TestAdminTokenHashIsSeparateAndOptIn(t *testing.T) {
	t.Setenv("CSX_ADMIN_TOKEN_SHA256", "")
	t.Setenv("CSX_GITHUB_CLIENT_SECRET", "github-secret")
	if got := ConfigFromEnv().AdminTokenSHA256; got != "" {
		t.Fatalf("AdminTokenSHA256 = %q, want disabled", got)
	}

	want := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CSX_ADMIN_TOKEN_SHA256", want)
	if got := ConfigFromEnv().AdminTokenSHA256; got != want {
		t.Fatalf("AdminTokenSHA256 = %q, want exact environment value", got)
	}
}
