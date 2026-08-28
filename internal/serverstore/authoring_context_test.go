package serverstore

import (
	"context"
	"testing"
	"time"
)

func TestAuthoringPollStatementTimeoutIsExplicitAndDeadlineBounded(t *testing.T) {
	if got := authoringPollStatementTimeout(context.Background()); got != 0 {
		t.Fatalf("ordinary background statement timeout = %v, want 0", got)
	}
	if got := authoringPollStatementTimeout(WithAuthoringPoll(context.Background())); got != authoringExpansionStatementTimeout {
		t.Fatalf("authoring statement timeout = %v, want %v", got, authoringExpansionStatementTimeout)
	}
	ctx, cancel := context.WithTimeout(WithAuthoringPoll(context.Background()), time.Second)
	defer cancel()
	if got := authoringPollStatementTimeout(ctx); got <= 0 || got >= time.Second {
		t.Fatalf("deadline-bounded authoring statement timeout = %v, want 0 < timeout < 1s", got)
	}
}
