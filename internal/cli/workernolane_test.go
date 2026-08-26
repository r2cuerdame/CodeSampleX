package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type noLaneFakeVerifier struct{ coordinates []string }

func (*noLaneFakeVerifier) RunOne(context.Context) (bool, error) { return false, nil }
func (*noLaneFakeVerifier) IsIdle() bool                         { return true }
func (f *noLaneFakeVerifier) UnsupportedWork() []string          { return f.coordinates }

// "completed=0 failed=0" was the only thing production said for three days
// while the queue held work no image in this build can run. The counter is
// honest and useless on its own: an operator cannot tell it apart from a
// queue that had nothing to offer, and only one of those two states is
// waiting for a human.
func TestWorkerSaysWhenTheOfferedWorkHasNoLaneHere(t *testing.T) {
	var out bytes.Buffer
	fake := &noLaneFakeVerifier{coordinates: []string{"golang go 1.27", "npm node 24"}}
	stats, err := runContributorWorker(context.Background(), fake, workerOptions{
		parallel: 1, budget: "unlimited", once: true, pollInterval: time.Millisecond,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want an idle run", stats)
	}
	text := out.String()
	if !strings.Contains(text, "golang go 1.27") || !strings.Contains(text, "npm node 24") {
		t.Fatalf("the coordinates nothing here can run were not reported:\n%s", text)
	}
	if !strings.Contains(text, "verifier") {
		t.Fatalf("the message does not say what is missing:\n%s", text)
	}
}

// An empty queue keeps its own sentence: nothing was skipped, so nothing is
// reported and the run is simply idle.
func TestWorkerSaysNothingExtraWhenTheQueueWasEmpty(t *testing.T) {
	var out bytes.Buffer
	fake := &noLaneFakeVerifier{}
	if _, err := runContributorWorker(context.Background(), fake, workerOptions{
		parallel: 1, budget: "unlimited", once: true, pollInterval: time.Millisecond,
	}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "verifier lane") {
		t.Fatalf("an empty queue was reported as unrunnable work:\n%s", out.String())
	}
}
