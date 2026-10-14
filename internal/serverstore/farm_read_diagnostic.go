package serverstore

import (
	"context"
	"errors"
	"time"
)

// FarmReadFailureReason is a fixed diagnostic code, never the source error.
// Callers must keep missing measurements unavailable and preserve backpressure.
func FarmReadFailureReason(err error) string {
	switch {
	case err == nil:
		return "none"
	case IsPoolBusy(err):
		return "pool_busy"
	case IsQueryTimeout(err):
		return "query_timeout"
	case IsQueryCanceled(err):
		return "query_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "read_failed"
	}
}

func farmReadBudgetMillis(ctx context.Context) int64 {
	if deadline, ok := ctx.Deadline(); ok {
		return max(0, time.Until(deadline).Milliseconds())
	}
	return -1
}
