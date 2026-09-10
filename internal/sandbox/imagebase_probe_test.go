package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSupportsLinuxContainersOnMatrix verifies the capability decision tree across
// OS combinations, environment flags, and daemon states.
func TestSupportsLinuxContainersOnMatrix(t *testing.T) {
	oldLook, oldProbe, oldServerOS := lookDocker, dockerProbe, dockerServerOSProbe
	t.Cleanup(func() {
		lookDocker, dockerProbe, dockerServerOSProbe = oldLook, oldProbe, oldServerOS
	})

	// 1. Windows without CSX_TEST_DOCKER skips fast without invoking probes.
	probesRun := 0
	lookDocker = func() error {
		probesRun++
		return nil
	}
	dockerProbe = func(ctx context.Context) error {
		probesRun++
		return nil
	}
	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) {
		probesRun++
		return []byte("linux"), nil
	}

	ok, reason := supportsLinuxContainersOn("windows", func(string) string { return "" }, context.Background())
	if ok {
		t.Fatal("supportsLinuxContainersOn(windows, no env) reported ok, want skip")
	}
	if !strings.Contains(reason, "unavailable on Windows") {
		t.Fatalf("reason = %q, want mention of unavailable on Windows", reason)
	}
	if probesRun != 0 {
		t.Fatalf("probesRun = %d, expected 0 fast-skip on Windows without CSX_TEST_DOCKER", probesRun)
	}

	// 2. Windows with CSX_TEST_DOCKER=1:
	// 2a. docker CLI missing
	lookDocker = func() error { return errors.New("no docker") }
	ok, reason = supportsLinuxContainersOn("windows", func(k string) string {
		if k == "CSX_TEST_DOCKER" {
			return "1"
		}
		return ""
	}, context.Background())
	if ok || reason != "docker not available" {
		t.Fatalf("got (%v, %q), want (false, docker not available)", ok, reason)
	}

	// 2b. docker daemon down
	lookDocker = func() error { return nil }
	dockerProbe = func(ctx context.Context) error { return errors.New("daemon down") }
	ok, reason = supportsLinuxContainersOn("windows", func(k string) string {
		if k == "CSX_TEST_DOCKER" {
			return "1"
		}
		return ""
	}, context.Background())
	if ok || reason != "docker daemon not available" {
		t.Fatalf("got (%v, %q), want (false, docker daemon not available)", ok, reason)
	}

	// 2c. docker daemon serving Windows containers
	dockerProbe = func(ctx context.Context) error { return nil }
	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) { return []byte("windows\n"), nil }
	ok, reason = supportsLinuxContainersOn("windows", func(k string) string {
		if k == "CSX_TEST_DOCKER" {
			return "1"
		}
		return ""
	}, context.Background())
	if ok || reason != "docker daemon does not support Linux containers" {
		t.Fatalf("got (%v, %q), want (false, docker daemon does not support Linux containers)", ok, reason)
	}

	// 2d. docker daemon serving Linux containers
	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) { return []byte("linux\n"), nil }
	ok, reason = supportsLinuxContainersOn("windows", func(k string) string {
		if k == "CSX_TEST_DOCKER" {
			return "1"
		}
		return ""
	}, context.Background())
	if !ok || reason != "" {
		t.Fatalf("got (%v, %q), want (true, \"\")", ok, reason)
	}

	// 3. Linux host:
	// 3a. docker CLI missing
	lookDocker = func() error { return errors.New("no docker") }
	ok, reason = supportsLinuxContainersOn("linux", func(string) string { return "" }, context.Background())
	if ok || reason != "docker not available" {
		t.Fatalf("got (%v, %q), want (false, docker not available)", ok, reason)
	}

	// 3b. docker daemon down
	lookDocker = func() error { return nil }
	dockerProbe = func(ctx context.Context) error { return errors.New("daemon down") }
	ok, reason = supportsLinuxContainersOn("linux", func(string) string { return "" }, context.Background())
	if ok || reason != "docker daemon not available" {
		t.Fatalf("got (%v, %q), want (false, docker daemon not available)", ok, reason)
	}

	// 3c. docker daemon serving Windows containers (e.g. invalid configuration)
	dockerProbe = func(ctx context.Context) error { return nil }
	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) { return []byte("windows\n"), nil }
	ok, reason = supportsLinuxContainersOn("linux", func(string) string { return "" }, context.Background())
	if ok || reason != "docker daemon does not support Linux containers" {
		t.Fatalf("got (%v, %q), want (false, docker daemon does not support Linux containers)", ok, reason)
	}

	// 3d. docker daemon serving Linux containers
	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) { return []byte("linux\n"), nil }
	ok, reason = supportsLinuxContainersOn("linux", func(string) string { return "" }, context.Background())
	if !ok || reason != "" {
		t.Fatalf("got (%v, %q), want (true, \"\")", ok, reason)
	}
}

// TestDetectContainerOSBoundedByTimeout asserts that DetectContainerOS does not
// block indefinitely if the underlying server OS probe hangs.
func TestDetectContainerOSBoundedByTimeout(t *testing.T) {
	oldServerOS, oldTimeout := dockerServerOSProbe, detectTimeout
	detectTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		dockerServerOSProbe = oldServerOS
		detectTimeout = oldTimeout
	})

	dockerServerOSProbe = func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	start := time.Now()
	// Pass un-deadline context to verify DetectContainerOS enforces its own detectTimeout.
	got := DetectContainerOS(context.Background())
	elapsed := time.Since(start)

	if got != ContainerOSLinux {
		t.Fatalf("got %q, want %q on timeout fallback", got, ContainerOSLinux)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("DetectContainerOS took %v, want <= ~100ms", elapsed)
	}
}
