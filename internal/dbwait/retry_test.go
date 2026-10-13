package dbwait

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// captureLog redirects the standard logger for the duration of fn and returns what it wrote.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

// TestRetry_FailsThenSucceeds is the real-world shape item 66 found: the first few attempts hit
// a real "connection refused" (nothing listening yet), then a listener comes up and the next
// attempt succeeds. Retry must ride out the refusals rather than treating the first one as fatal.
func TestRetry_FailsThenSucceeds(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	const failures = 2
	attempts := 0
	fn := func() error {
		attempts++
		if attempts <= failures {
			conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if dialErr != nil {
				return dialErr
			}
			conn.Close()
			t.Fatalf("dial unexpectedly succeeded on attempt %d against a closed listener", attempts)
		}
		return nil
	}

	err = Retry(context.Background(), "connecting to test db", 5*time.Second, fn)
	if err != nil {
		t.Fatalf("Retry() = %v, want nil once fn starts succeeding", err)
	}
	if attempts != failures+1 {
		t.Errorf("attempts = %d, want exactly %d", attempts, failures+1)
	}
}

// TestRetry_NeverSucceeds confirms a connection-level failure that never clears still fails --
// loudly, with the underlying error wrapped in and the attempt count reported -- rather than
// retrying forever, once maxElapsed (overridden short here) has passed.
func TestRetry_NeverSucceeds(t *testing.T) {
	wantErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	attempts := 0
	err := Retry(context.Background(), "connecting to nowhere", 1200*time.Millisecond, func() error {
		attempts++
		return wantErr
	})
	if err == nil {
		t.Fatal("Retry() = nil, want an error once maxElapsed has passed")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Retry() = %v, want it to wrap %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "connecting to nowhere") {
		t.Errorf("Retry() = %v, want it to name what it was trying to do", err)
	}
	if attempts < 2 {
		t.Errorf("attempts = %d, want more than 1 before giving up", attempts)
	}
}

// TestRetry_NonRetryableFailsAtOnce confirms a wrong-password error (SQLSTATE 28P01) -- which no
// amount of waiting will fix -- returns on the very first attempt instead of being retried into
// a misleading "still failing after Ns" timeout.
func TestRetry_NonRetryableFailsAtOnce(t *testing.T) {
	wantErr := &pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"}
	attempts := 0
	start := time.Now()
	err := Retry(context.Background(), "connecting to test db", time.Minute, func() error {
		attempts++
		return wantErr
	})
	elapsed := time.Since(start)

	if !errors.Is(err, wantErr) {
		t.Errorf("Retry() = %v, want it to wrap %v", err, wantErr)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want exactly 1 (a non-retryable error must not be retried)", attempts)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %s, want Retry to return immediately on a non-retryable error", elapsed)
	}
}

// TestRetry_LogsEachRetry confirms an operator watching logs at boot sees the wait, not a
// silent hang or an immediate crash.
func TestRetry_LogsEachRetry(t *testing.T) {
	logged := captureLog(t, func() {
		_ = Retry(context.Background(), "connecting to test db", 600*time.Millisecond, func() error {
			return &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		})
	})
	if !strings.Contains(logged, "connecting to test db") {
		t.Errorf("log output = %q, want it to mention what Retry was trying to do", logged)
	}
	if !strings.Contains(logged, "retrying in") {
		t.Errorf("log output = %q, want it to report a retry", logged)
	}
}

// TestRetry_StopsOnContextCancellation confirms Retry doesn't keep sleeping past a canceled
// context, even if maxElapsed hasn't been reached yet and the error is otherwise retryable.
func TestRetry_StopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	start := time.Now()
	err := Retry(ctx, "x", time.Minute, func() error {
		attempts++
		if attempts == 1 {
			// Cancel only after the first attempt, mid-wait, so Retry is actually sleeping
			// (not just observing an already-canceled context) when cancellation lands.
			cancel()
		}
		return &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Retry() = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed = %s, want Retry to return promptly once ctx is canceled", elapsed)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want exactly 1 (canceled during the wait after the first attempt)", attempts)
	}
}

// TestIsRetryable_Classification confirms the error categories the task names explicitly:
// connection-level failures are retried, everything else is not.
func TestIsRetryable_Classification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"dns error", &net.DNSError{Err: "no such host", Name: "nowhere.invalid", IsNotFound: true}, true},
		{"postgres starting up", &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}, true},
		{"wrong password", &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, false},
		{"missing database", &pgconn.PgError{Code: "3D000", Message: "database \"x\" does not exist"}, false},
		{"sql syntax error", &pgconn.PgError{Code: "42601", Message: "syntax error"}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryable(c.err); got != c.want {
				t.Errorf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
