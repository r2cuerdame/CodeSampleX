package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
)

func init() {
	Register(Command{
		Name:    "ui",
		Summary: "open the local dashboard (starts the daemon if needed)",
		Run:     uiMain,
	})
}

// uiMain implements `csx ui [--open|--no-open]` (§12.5): ensure the
// daemon is up, print the dashboard URL, and open it in the default
// browser unless --no-open (tests, headless setups) was given.
func uiMain(ctx context.Context, args []string) int {
	open := true
	for _, a := range args {
		switch a {
		case "--no-open":
			open = false
		case "--open":
			open = true
		default:
			fmt.Fprintln(os.Stderr, "usage: csx ui [--open|--no-open]")
			return 2
		}
	}

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	c, err := daemon.EnsureRunning(ctx, home, Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	url := c.BaseURL + "/ui"
	fmt.Println(url)
	if open {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "csx: open browser: %v (open %s manually)\n", err, url)
		}
	}
	return 0
}

// openBrowser launches the platform default browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		// `start` is a cmd builtin; the empty string is the window title.
		return exec.Command("cmd", "/c", "start", "", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
