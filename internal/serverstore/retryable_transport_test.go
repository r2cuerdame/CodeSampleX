package serverstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type fakeNetTimeout struct{}

func (fakeNetTimeout) Error() string   { return "dial tcp: i/o timeout" }
func (fakeNetTimeout) Timeout() bool   { return true }
func (fakeNetTimeout) Temporary() bool { return true }

// The retry class is the narrow one: only a failure of the path to
// PostgreSQL earns a second attempt. Every "not now" this process or
// PostgreSQL produces about saturation is transient for status purposes and
// still not retried, because the first attempt already cost the ceiling.
func TestIsRetryableTransportErrorIsNarrowerThanTransient(t *testing.T) {
	var netErr net.Error = fakeNetTimeout{}
	cases := []struct {
		name      string
		err       error
		transient bool
		retryable bool
	}{
		{"nil", nil, false, false},
		{"pool busy", ErrPoolBusy, true, false},
		{"admission refusal", fmt.Errorf("%w (package cache-miss admission)", ErrPoolBusy), true, false},
		{"deferred lane", fmt.Errorf("%w (snapshot cache load deferred)", ErrPoolBusy), true, false},
		{"follow-up suppressed", fmt.Errorf("%w (class interactive, follow-up suppressed after earlier backpressure)", ErrPoolBusy), true, false},
		{"statement ceiling", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, true, false},
		{"deadline exceeded", context.DeadlineExceeded, true, false},
		{"canceled", context.Canceled, false, false},
		{"deterministic", errors.New("syntax error at or near"), false, false},
		{"connection exception", &pgconn.PgError{Code: "08006", Message: "connection failure"}, false, true},
		{"net timeout", netErr, true, true},
		{"connection refused", errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"), true, true},
		{"unexpected EOF", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true, true},
		{"conn closed", errors.New("conn closed"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientReadError(tc.err); got != tc.transient {
				t.Errorf("IsTransientReadError = %v, want %v", got, tc.transient)
			}
			if got := IsRetryableTransportError(tc.err); got != tc.retryable {
				t.Errorf("IsRetryableTransportError = %v, want %v", got, tc.retryable)
			}
		})
	}
}
