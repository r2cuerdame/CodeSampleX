package environment

import "testing"

func TestParseUserAgent(t *testing.T) {
	cases := []struct {
		name, ua                      string
		family, major, engine, engMaj string
	}{
		{"chrome-win", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
			"chrome", "140", "chromium", "140"},
		{"edge", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0",
			"edge", "140", "chromium", "140"},
		{"firefox", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:142.0) Gecko/20100101 Firefox/142.0",
			"firefox", "142", "gecko", "142"},
		{"safari-mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15",
			"safari", "19", "webkit", "605"},
		{"android-webview", "Mozilla/5.0 (Linux; Android 15; Pixel 9; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/140.0.0.0 Mobile Safari/537.36",
			"android-webview", "140", "chromium", "140"},
		{"ios-wkwebview", "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
			"ios-wkwebview", "", "webkit", "605"},
		{"electron", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) myapp/1.0.0 Chrome/136.0.0.0 Electron/38.0.0 Safari/537.36",
			"electron", "38", "chromium", "136"},
		{"unknown", "curl/8.9.1", "", "", "", ""},
	}
	for _, c := range cases {
		got := ParseUserAgent(c.ua)
		if got.Family != c.family || got.Major != c.major || got.Engine != c.engine || got.EngineVersion != c.engMaj {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestCollectBrowserHints(t *testing.T) {
	fp := Collect(t.Context(), map[string]string{
		"ecosystem": "npm",
		"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	})
	if fp.ExecutionContext != "browser" || fp.BrowserFamily != "chrome" || fp.BrowserMajor != "140" || fp.Engine != "chromium" {
		t.Fatalf("browser context not derived: %+v", fp)
	}
	if fp.ContextLabel() != "chrome 140" {
		t.Errorf("ContextLabel = %q", fp.ContextLabel())
	}
}

func TestCollectNodeContextNormalized(t *testing.T) {
	fp := Collect(t.Context(), map[string]string{"ecosystem": "npm", "runtime": "node", "runtimeVersion": "22.18"})
	if fp.ExecutionContext != "node" {
		t.Fatalf("node runtime must normalize executionContext, got %+v", fp)
	}
	if fp.ContextLabel() != "node 22.18" {
		t.Errorf("ContextLabel = %q", fp.ContextLabel())
	}
}
