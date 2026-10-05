package servertiming

import (
	"context"
	"testing"
	"time"
)

func TestDetachedPoolWaitDoesNotCountTowardRequest(t *testing.T) {
	ctx, timing := Start(context.Background())
	AddPoolWait(Detached(ctx), 10*time.Millisecond)
	AddPoolWait(ctx, time.Millisecond)
	if timing.poolWait != time.Millisecond {
		t.Fatalf("request pool wait = %s, want only the request's 1ms wait", timing.poolWait)
	}
}
