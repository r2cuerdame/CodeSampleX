package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestWorkerRefusesLocalOnlyBeforeDockerOrNetworkWork(t *testing.T) {
	var out, errOut bytes.Buffer
	detectCalls := 0
	env := &workerEnv{
		stdout: &out,
		stderr: &errOut,
		home:   func() (string, error) { return t.TempDir(), nil },
		load: func(string) (*config.Config, error) {
			cfg := config.Default()
			cfg.Mode = config.ModeLocalOnly
			return cfg, nil
		},
		detect: func(context.Context) domain.SandboxCapability {
			detectCalls++
			return domain.CapContainerRun
		},
	}
	if code := workerMainWith(context.Background(), []string{"start", "--once"}, env); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if detectCalls != 0 {
		t.Fatalf("Docker detection ran %d times in local-only mode", detectCalls)
	}
	if !strings.Contains(errOut.String(), "COMMUNITY") || !strings.Contains(errOut.String(), "no network request") {
		t.Fatalf("refusal was not explicit:\n%s", errOut.String())
	}
}

func TestWorkerRefusesWhenDockerIsUnavailable(t *testing.T) {
	var out, errOut bytes.Buffer
	ensured := false
	env := &workerEnv{
		stdout: &out,
		stderr: &errOut,
		home:   func() (string, error) { return t.TempDir(), nil },
		load: func(string) (*config.Config, error) {
			cfg := config.Default()
			cfg.Mode = config.ModeCommunity
			return cfg, nil
		},
		detect: func(context.Context) domain.SandboxCapability { return domain.CapCompileOnly },
		ensure: func(string) error {
			ensured = true
			return nil
		},
	}
	if code := workerMainWith(context.Background(), []string{"start", "--once"}, env); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if ensured {
		t.Fatal("worker continued setup after Docker refusal")
	}
	if !strings.Contains(errOut.String(), "Docker") || !strings.Contains(errOut.String(), "never used as a fallback") {
		t.Fatalf("Docker refusal was not explicit:\n%s", errOut.String())
	}
}

func TestWorkerValidatesParallelAndModeBeforeSetup(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"start", "--parallel=0"}, "between 1 and 8"},
		{[]string{"start", "--parallel=9"}, "between 1 and 8"},
		{[]string{"start", "--mode=expand"}, "unavailable"},
		{[]string{"start", "--mode=create"}, "unavailable"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			env := &workerEnv{stdout: &out, stderr: &errOut}
			if code := workerMainWith(context.Background(), tc.args, env); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", errOut.String(), tc.want)
			}
		})
	}
}

func TestWorkerHelpStatesSafeMVPBoundary(t *testing.T) {
	var out, errOut bytes.Buffer
	env := &workerEnv{stdout: &out, stderr: &errOut}
	if code := workerMainWith(context.Background(), []string{"--help"}, env); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	help := out.String()
	for _, want := range []string{"csx worker start", "reason=cross", "Docker", "EXPAND", "CREATE", "--parallel", "--budget", "--once"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}

type queuedFakeVerifier struct {
	remaining atomic.Int64
	calls     atomic.Int64
}

func (f *queuedFakeVerifier) RunOne(context.Context) (bool, error) {
	f.calls.Add(1)
	for {
		n := f.remaining.Load()
		if n <= 0 {
			return false, nil
		}
		if f.remaining.CompareAndSwap(n, n-1) {
			return true, nil
		}
	}
}

func (*queuedFakeVerifier) IsIdle() bool { return true }

func TestWorkerOnceProcessesAtMostOneJob(t *testing.T) {
	fake := &queuedFakeVerifier{}
	fake.remaining.Store(5)
	stats, err := runContributorWorker(context.Background(), fake, workerOptions{
		parallel: 8, budget: "unlimited", once: true, pollInterval: time.Millisecond,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 1 || stats.Failed != 0 || fake.calls.Load() != 1 {
		t.Fatalf("stats=%+v calls=%d", stats, fake.calls.Load())
	}
}

type parallelFakeVerifier struct {
	next      atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
	total     int64
	cancel    context.CancelFunc
}

func (f *parallelFakeVerifier) RunOne(context.Context) (bool, error) {
	n := f.next.Add(1)
	if n > f.total {
		return false, nil
	}
	active := f.active.Add(1)
	for {
		old := f.maxActive.Load()
		if active <= old || f.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	f.active.Add(-1)
	if n == f.total {
		f.cancel()
	}
	return true, nil
}

func (*parallelFakeVerifier) IsIdle() bool { return true }

func TestWorkerRunsVerificationLanesInParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &parallelFakeVerifier{total: 8, cancel: cancel}
	stats, err := runContributorWorker(ctx, fake, workerOptions{
		parallel: 4, budget: "unlimited", pollInterval: time.Millisecond,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 8 {
		t.Fatalf("completed = %d, want 8", stats.Completed)
	}
	if got := fake.maxActive.Load(); got < 2 || got > 4 {
		t.Fatalf("maximum parallelism = %d, want 2..4", got)
	}
}

type cancelFakeVerifier struct{ entered chan struct{} }

func (f *cancelFakeVerifier) RunOne(ctx context.Context) (bool, error) {
	select {
	case <-f.entered:
	default:
		close(f.entered)
	}
	<-ctx.Done()
	return false, ctx.Err()
}

func (*cancelFakeVerifier) IsIdle() bool { return true }

func TestWorkerStopsCleanlyWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &cancelFakeVerifier{entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := runContributorWorker(ctx, fake, workerOptions{
			parallel: 1, budget: "unlimited", pollInterval: time.Hour,
		}, &bytes.Buffer{})
		done <- err
	}()
	select {
	case <-fake.entered:
	case <-time.After(time.Second):
		t.Fatal("worker never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("stop error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestWorkerBudgetNamesHaveExactDurations(t *testing.T) {
	for budget, want := range map[string]time.Duration{
		"5m": 5 * time.Minute, "15m": 15 * time.Minute,
		"idle": 0, "unlimited": 0,
	} {
		got, ok := workerBudgetDuration(budget)
		if !ok || got != want {
			t.Errorf("budget %q = %v,%v; want %v,true", budget, got, ok, want)
		}
	}
	if _, ok := workerBudgetDuration("1h"); ok {
		t.Fatal("unsupported duration was accepted")
	}
}
