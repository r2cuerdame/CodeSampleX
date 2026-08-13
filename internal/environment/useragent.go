package environment

import (
	"regexp"
	"strings"
)

// BrowserContext is the normalized, privacy-reduced form of a browser
// runtime. The full User-Agent string is parsed locally and then discarded:
// only these coarse fields may enter Evidence (goal — API compatibility,
// not fingerprinting).
type BrowserContext struct {
	Family        string // chrome|edge|firefox|safari|chromium|android-webview|ios-wkwebview|electron
	Major         string // "140"
	Engine        string // chromium|gecko|webkit
	EngineVersion string // engine major, e.g. "140" or "605"
}

var (
	reElectron   = regexp.MustCompile(`Electron/(\d+)`)
	reEdge       = regexp.MustCompile(`Edg[eA]?/(\d+)`)
	reOPR        = regexp.MustCompile(`OPR/(\d+)`)
	reChrome     = regexp.MustCompile(`Chrome/(\d+)`)
	reFirefox    = regexp.MustCompile(`Firefox/(\d+)`)
	reSafariVer  = regexp.MustCompile(`Version/(\d+)`)
	reWebKit     = regexp.MustCompile(`AppleWebKit/(\d+)`)
	reAndroidWV  = regexp.MustCompile(`; wv\)`)
	reMobileiOS  = regexp.MustCompile(`\((iPhone|iPad|iPod)`)
	reSafariTok  = regexp.MustCompile(`Safari/\d`)
	reHeadless   = regexp.MustCompile(`HeadlessChrome/(\d+)`)
)

// ParseUserAgent normalizes a User-Agent into a BrowserContext.
// Unrecognized agents return a zero value — an unknown context is honest,
// a guessed one is not.
func ParseUserAgent(ua string) BrowserContext {
	if ua == "" {
		return BrowserContext{}
	}
	pick := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(ua); m != nil {
			return m[1]
		}
		return ""
	}
	switch {
	case reElectron.MatchString(ua):
		return BrowserContext{Family: "electron", Major: pick(reElectron), Engine: "chromium", EngineVersion: pick(reChrome)}
	case reAndroidWV.MatchString(ua) && reChrome.MatchString(ua):
		return BrowserContext{Family: "android-webview", Major: pick(reChrome), Engine: "chromium", EngineVersion: pick(reChrome)}
	case reEdge.MatchString(ua):
		return BrowserContext{Family: "edge", Major: pick(reEdge), Engine: "chromium", EngineVersion: pick(reChrome)}
	case reOPR.MatchString(ua):
		return BrowserContext{Family: "chromium", Major: pick(reOPR), Engine: "chromium", EngineVersion: pick(reChrome)}
	case reHeadless.MatchString(ua):
		return BrowserContext{Family: "chromium", Major: pick(reHeadless), Engine: "chromium", EngineVersion: pick(reHeadless)}
	case reChrome.MatchString(ua):
		return BrowserContext{Family: "chrome", Major: pick(reChrome), Engine: "chromium", EngineVersion: pick(reChrome)}
	case reFirefox.MatchString(ua):
		return BrowserContext{Family: "firefox", Major: pick(reFirefox), Engine: "gecko", EngineVersion: pick(reFirefox)}
	case reMobileiOS.MatchString(ua) && reWebKit.MatchString(ua) && !reSafariTok.MatchString(ua):
		// WKWebView UAs carry AppleWebKit but no Safari/ token.
		return BrowserContext{Family: "ios-wkwebview", Major: pick(reSafariVer), Engine: "webkit", EngineVersion: pick(reWebKit)}
	case reWebKit.MatchString(ua) && strings.Contains(ua, "Safari"):
		return BrowserContext{Family: "safari", Major: pick(reSafariVer), Engine: "webkit", EngineVersion: pick(reWebKit)}
	}
	return BrowserContext{}
}
