package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
)

func init() {
	Register(Command{
		Name:    "daemon",
		Summary: "control the local daemon: run | start | stop | status",
		Run:     daemonMain,
	})
}

func daemonMain(ctx context.Context, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}

	switch sub {
	case "run":
		return daemonRun(ctx, home)
	case "start":
		c, err := daemon.EnsureRunning(ctx, home, Version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx: start daemon: %v\n", err)
			return 1
		}
		fmt.Printf("csx daemon running at %s\n", c.BaseURL)
		return 0
	case "stop":
		c, err := daemon.NewClient(home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx: %v\n", err)
			return 1
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := c.Shutdown(sctx); err != nil {
			fmt.Println("csx daemon is not running")
			return 0
		}
		fmt.Println("csx daemon stopping")
		return 0
	case "status":
		c, err := daemon.NewClient(home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx: %v\n", err)
			return 1
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		st, err := c.Status(sctx)
		if err != nil {
			fmt.Println("csx daemon: not running")
			return 1
		}
		fmt.Printf("csx daemon: running\n")
		fmt.Printf("  version:     %s\n", st.Version)
		fmt.Printf("  mode:        %s\n", modeOrUninitialized(st.Mode))
		fmt.Printf("  home:        %s\n", st.Home)
		fmt.Printf("  peer id:     %s\n", st.PeerID)
		fmt.Printf("  uptime:      %s\n", st.Uptime)
		fmt.Printf("  queue depth: %d\n", st.QueueDepth)
		if st.LastUpload != "" {
			fmt.Printf("  last upload: %s\n", st.LastUpload)
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: csx daemon run|start|stop|status")
		return 2
	}
}

// daemonRun is the foreground daemon (what `csx daemon start` spawns
// detached). It stops gracefully on Ctrl-C / SIGTERM-equivalent.
func daemonRun(ctx context.Context, home string) int {
	daemon.Version = Version
	d, err := daemon.New(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: daemon: %v\n", err)
		return 1
	}
	defer d.Close()

	nctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	go func() {
		select {
		case <-d.Ready():
			fmt.Printf("csx daemon listening on %s (ui: %s/ui)\n", d.BaseURL(), d.BaseURL())
		case <-nctx.Done():
		}
	}()
	if err := d.Run(nctx); err != nil {
		fmt.Fprintf(os.Stderr, "csx: daemon: %v\n", err)
		return 1
	}
	return 0
}

func modeOrUninitialized(mode string) string {
	if mode == "" {
		return "(uninitialized — run csx init)"
	}
	return mode
}
