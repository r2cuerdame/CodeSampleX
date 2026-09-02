package cli

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A worker with nothing to do has to say so.
//
// Reported from csx-farm-linux-1: 140 jobs completed in 24 hours and then
// "0 seconds of CPU, idle since restart", with not one line in the log --
// no "no work", no "waiting", no "idle". From outside a verifier that is
// correctly idle and one that has hung look identical, and that node spent a
// day unable to tell which it was.
//
// The exclusion rule makes idleness the EXPECTED state on a single-node farm:
// a peer holding a receipt for a sample is not offered that sample's cross
// job again, and a node that authored nearly everything has almost nothing
// left it is allowed to verify. Silence is the wrong way to report a designed
// outcome.
func TestAnIdleWorkerSaysSo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &idleFakeVerifier{cancelAfter: 3, cancel: cancel}
	var out bytes.Buffer
	if _, err := runContributorWorker(ctx, fake, workerOptions{
		parallel: 1, budget: "unlimited", pollInterval: time.Millisecond,
	}, &out); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "no work") {
		t.Errorf("an idle worker printed nothing about being idle:\n%s", got)
	}
}

// And it must not say it on every poll. The poll interval is seconds; a line
// per poll is a log nobody reads, which is the same failure as silence
// wearing different clothes.
func TestAnIdleWorkerDoesNotSayItEveryPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &idleFakeVerifier{cancelAfter: 40, cancel: cancel}
	var out bytes.Buffer
	if _, err := runContributorWorker(ctx, fake, workerOptions{
		parallel: 1, budget: "unlimited", pollInterval: time.Millisecond,
	}, &out); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	lines := strings.Count(out.String(), "no work")
	if lines == 0 {
		t.Fatal("an idle worker printed nothing across 40 polls")
	}
	if lines > 3 {
		t.Errorf("40 empty polls produced %d idle lines; the heartbeat is not bounded", lines)
	}
}

type idleFakeVerifier struct {
	polls       atomic.Int64
	cancelAfter int64
	cancel      context.CancelFunc
}

func (f *idleFakeVerifier) RunOne(context.Context) (bool, error) {
	if f.polls.Add(1) >= f.cancelAfter {
		f.cancel()
	}
	return false, nil // the queue has nothing this peer may take
}

func (*idleFakeVerifier) IsIdle() bool { return true }
