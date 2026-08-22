package main

import (
	"fmt"
	"net"
)

// resolveListenAddr decides the address the HTTP server actually binds,
// given CSX_LISTEN and the target operating system.
//
// A listen value with no host — ":8080" — binds every interface. That is
// the container contract: Caddy reaches the service over the compose
// network, and the container is Linux. On Windows the same value means
// something else entirely. Nothing there proxies to this process, so the
// only caller is the developer or agent who started it, and binding every
// interface has two costs: it raises the Windows Defender Firewall consent
// dialog, and it puts an admin-capable API backed by a local database on
// the local network for the length of the session.
//
// The dialog is the part that recurs. It identifies a program by its
// executable path, and a locally built csx-server lives in a scratch
// directory that changes with every build, so the allow decision is never
// remembered and the prompt returns under whatever name that build used —
// csx-server-new.exe among them (R2C-84).
//
// So on Windows an unspecified host is narrowed to loopback. A host the
// operator actually wrote is honoured exactly as written, 0.0.0.0 and [::]
// included: choosing to serve the network stays a choice, it just has to
// be made out loud. Anything this cannot parse is returned untouched so
// the listener reports the real error rather than binding somewhere the
// operator never asked for.
func resolveListenAddr(listen, goos string) (addr string, narrowed bool) {
	if goos != "windows" {
		return listen, false
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host != "" {
		return listen, false
	}
	return net.JoinHostPort("127.0.0.1", port), true
}

// narrowedListenNotice is what the server says when it binds somewhere
// other than what CSX_LISTEN literally asked for. It names the setting, the
// address actually bound, and the value to write to get the other
// behaviour — an operator who does want the local network should not have
// to read this source to find out how.
func narrowedListenNotice(listen, addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8080"
	}
	return fmt.Sprintf("csx-server: CSX_LISTEN=%q names no host, which on windows binds every interface and raises the firewall consent dialog once per executable path; listening on %s instead. Set CSX_LISTEN=%s to serve the local network deliberately.",
		listen, addr, net.JoinHostPort("0.0.0.0", port))
}
