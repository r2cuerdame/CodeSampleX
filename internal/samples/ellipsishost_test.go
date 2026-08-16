package samples

import "testing"

// A URL whose host is pure punctuation is a placeholder in prose, not a
// hostname. It refused a real sample: an axios doc comment explaining that
// `proxy: 'http://...'` is invalid configuration was read as a URL pointing
// at the host "...".
//
// The publish gate has no override, so a false positive here does not cost
// a warning — it costs the whole sample.
func TestEllipsisHostIsNotAHost(t *testing.T) {
	for _, raw := range []string{
		"http://...",
		"https://...",
		"http://.../path",
	} {
		if !urlAllowed(raw, nil) {
			t.Errorf("%q was refused; a host with no letter or digit in it "+
				"cannot be anybody's internal hostname", raw)
		}
	}
}

// The guard it is carved out of must not move. Anything with a letter in
// it could be a real internal name, and stays refused.
func TestInventedHostsAreStillRefused(t *testing.T) {
	for _, raw := range []string{
		"http://proxy:8080",
		"http://internal-registry/",
		"https://a.../x",
		"http://acme-corp.internal/v1",
		// The empty host an interpolation leaves behind is not a
		// punctuation placeholder; it is a hidden host.
		"http://$secretHost/admin",
	} {
		if urlAllowed(raw, nil) {
			t.Errorf("%q was allowed; nothing can tell it from a company's own host", raw)
		}
	}
}
