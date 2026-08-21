package admin

import "strings"

// The Linux verification worker install command lives here rather than on a
// public page.
//
// They were the call to action on /contribute, which is gone: a contributor
// turned out to do exactly what any user does -- installing csx is what emits
// observations -- except for running samples to verify them, and the project
// runs those on its own machines. So this is an operator tool, and it belongs
// beside the internal sample workers it pairs with.
//
// The Windows variants are gone with the Windows verification farm. Only
// golang and pypi publish a Windows base image at all, so that axis could
// never have been more than two ecosystems wide -- which makes a Windows cell
// observation-only by design rather than a backlog anyone can work off.
func WorkerUnixCMD(base string) string {
	installer := strings.TrimRight(base, "/") + "/install.sh"
	// The variable rides on the sh that RUNS install.sh, not on curl: in
	// POSIX sh a leading VAR=1 binds to the first command alone, so
	// `CSX_WORKER_ONLY=1 curl … | sh` ran the full interactive init.
	return "curl -fsSL " + installer + " | CSX_WORKER_ONLY=1 sh && " +
		"csx worker start --mode verify --parallel 2 --budget idle"
}

// workerUnixRunCMD reports what is installed and running, then starts the
// worker. The status lines come first because `csx worker start` runs in
// the foreground and never returns while it is working — after it, nothing
// else in the paste would ever print.
func WorkerUnixRunCMD() string {
	return "csx version && csx daemon status\n" +
		"csx worker start --mode verify --parallel 2 --budget idle"
}

// workerInstallBase is where the installers are published. The admin page is
// reached over whatever host the operator typed, which may be an IP or a
// tunnel, so the command must not be built from the request.
const workerInstallBase = "https://codesamplex.dev"
