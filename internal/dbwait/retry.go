// Package dbwait retries a database's first connection at process startup.
// ARCHITECTURE.md item 66 (c): a freshly-scheduled pod's first connection to PostgreSQL can be
// refused for about 3 seconds while k3s's kube-router finishes adding the new pod's IP to the
// NetworkPolicy allow set. booth-catalog used to exit on that first refusal, crash-looping until
// a restart happened to land after the window closed. Retrying for about a minute rides out that
// transient window instead; a connection-level failure still happening after that long is a real
// outage, not a scheduling race, so Retry's caller should treat its error as fatal.
//
// This mirrors the shape of booth-core's internal/dbwait/retry.go (not imported here: a separate
// module, and ADR 0001 keeps each module's startup self-contained). Unlike that package, Retry
// classifies the error itself before deciding to retry: booth-core only ever wraps a bare
// connectivity probe, but booth-catalog's first connection also carries credentials and a
// database name, so a wrong password or a missing database must fail immediately rather than
// retrying for a minute and then reporting a misleading "still failing" timeout.
package dbwait

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultMaxElapsed bounds the total time Retry spends retrying before giving up: comfortably
// longer than the few-second refusal window item 66 observed, short enough that a genuinely
// unreachable server still fails loudly within about a minute rather than hanging indefinitely.
const DefaultMaxElapsed = 60 * time.Second

const (
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
)

// Retry calls fn, which should attempt exactly one connection or connection-dependent
// operation, retrying with exponential backoff (starting at 500ms, doubling up to 8s) as long
// as fn's error is connection-level -- refused, timed out, unresolved DNS, or Postgres reporting
// it is still starting up -- and maxElapsed hasn't passed since the first attempt. Any other
// error (wrong credentials, a missing database, a SQL error) returns immediately: no amount of
// waiting fixes those, so retrying would only hide a real misconfiguration behind what looks
// like the startup window this exists to ride out. ctx cancellation is also immediate. Each
// retry is logged with what, the attempt number, the error, and the delay before the next
// attempt. A non-nil return wraps fn's last error (or ctx's) with the number of attempts made.
func Retry(ctx context.Context, what string, maxElapsed time.Duration, fn func() error) error {
	deadline := time.Now().Add(maxElapsed)
	backoff := initialBackoff

	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return fmt.Errorf("%s: %w", what, err)
		}

		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s: still failing after %s (%d attempts): %w", what, maxElapsed, attempt, err)
		}

		log.Printf("%s: not ready yet (attempt %d: %v); retrying in %s", what, attempt, err, backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// isRetryable reports whether err is a connection-level failure worth waiting out: refused,
// timed out, an unresolved host, or Postgres's "the database system is starting up" (SQLSTATE
// 57P03). Everything else -- a wrong password (28P01), a missing database (3D000), any other
// SQL error -- is a real misconfiguration that no amount of retrying will fix, so it is reported
// at once instead of being retried into a confusing timeout.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57P03" // cannot_connect_now
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return false
}
