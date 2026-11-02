package serverstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestFarmReadFailureReasonUsesTypesAndNeverErrorText(t *testing.T) {
	const secret = "PRIVATE_CANARY_session_sql_payload"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "none"},
		{"wrapped pool", fmt.Errorf("%s: %w", secret, ErrPoolBusy), "pool_busy"},
		{"statement timeout", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout", Detail: secret}, "query_timeout"},
		{"client query cancel", &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request", Detail: secret}, "query_canceled"},
		{"wrapped deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "deadline"},
		{"wrapped cancellation", fmt.Errorf("%s: %w", secret, context.Canceled), "canceled"},
		{"unknown private error", errors.New(secret), "read_failed"},
		{"timeout words are not a type", errors.New("canceling statement due to statement timeout " + secret), "read_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FarmReadFailureReason(tc.err)
			if got != tc.want || strings.Contains(got, secret) {
				t.Fatalf("reason=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestFarmReadBudgetKeepsExpiredCallerAtZero(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := farmReadBudgetMillis(ctx); got != 0 {
		t.Fatalf("expired remaining budget=%d", got)
	}
	if got := farmReadBudgetMillis(context.Background()); got != -1 {
		t.Fatalf("absent deadline=%d", got)
	}
}
