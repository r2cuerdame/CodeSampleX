package main

import "testing"

// R2C-84. The Windows Defender Firewall consent dialog is raised by binding
// an *unspecified* host, and it identifies the program by executable path.
// A developer or agent build lands in a fresh temporary directory every
// time, so the allow decision can never be remembered: it prompts again on
// every rebuild, under whatever filename that build happened to use.
//
// Three such prompts are on this project's record, all from one agent
// scratchpad and all from `csx-server` cross-built for Windows and run with
// an explicit port but no host: csx-server.exe, csx-server-old.exe and
// csx-server-new.exe, logged as "listening on :8099", ":8098" and ":8097".
// The last of those is the name the bug report saw.
//
// A wildcard bind is correct in exactly one place — the container, where
// Caddy dials the service over the compose network — and that place is
// Linux. On Windows an unspecified host means a developer running this
// locally, so bind loopback. Naming a host stays an operator decision and
// is honoured verbatim, including a deliberate 0.0.0.0.
func TestListenAddrNarrowsUnspecifiedHostToLoopbackOnWindowsOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		listen   string
		goos     string
		want     string
		narrowed bool
	}{
		{"windows default port only", ":8080", "windows", "127.0.0.1:8080", true},
		{"windows dev port only", ":8097", "windows", "127.0.0.1:8097", true},
		{"linux keeps the container contract", ":8080", "linux", ":8080", false},
		{"darwin keeps the wildcard", ":8080", "darwin", ":8080", false},
		{"explicit wildcard is an operator decision", "0.0.0.0:8080", "windows", "0.0.0.0:8080", false},
		{"explicit v6 wildcard is an operator decision", "[::]:8080", "windows", "[::]:8080", false},
		{"already loopback", "127.0.0.1:8080", "windows", "127.0.0.1:8080", false},
		{"named host", "localhost:8080", "windows", "localhost:8080", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, narrowed := resolveListenAddr(tc.listen, tc.goos)
			if got != tc.want || narrowed != tc.narrowed {
				t.Errorf("resolveListenAddr(%q, %q) = (%q, %v), want (%q, %v)",
					tc.listen, tc.goos, got, narrowed, tc.want, tc.narrowed)
			}
		})
	}
}

// A malformed CSX_LISTEN is passed through untouched so the listener
// reports the real error. Guessing at a repair here would turn a clear
// "address 8080: missing port in address" into a server quietly listening
// somewhere the operator did not ask for.
func TestListenAddrPassesMalformedValuesThroughUnchanged(t *testing.T) {
	for _, listen := range []string{"8080", "", "127.0.0.1:80:80"} {
		if got, narrowed := resolveListenAddr(listen, "windows"); got != listen || narrowed {
			t.Errorf("resolveListenAddr(%q, windows) = (%q, %v), want (%q, false)", listen, got, narrowed, listen)
		}
	}
}

// The notice has to name the variable, what it would have done, and the way
// out. A line that only says "narrowed to loopback" leaves an operator who
// genuinely wants the local network with no idea what to type.
func TestNarrowedListenNoticeNamesTheSettingAndTheWayOut(t *testing.T) {
	got := narrowedListenNotice(":8080", "127.0.0.1:8080")
	for _, want := range []string{"CSX_LISTEN", ":8080", "127.0.0.1:8080", "0.0.0.0:8080", "firewall"} {
		if !contains(got, want) {
			t.Errorf("notice %q does not mention %q", got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
