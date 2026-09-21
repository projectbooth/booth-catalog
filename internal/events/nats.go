package events

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// StreamName is the shared JetStream stream booth-core creates and owns (ADR 0026). This
// module only ever consumes from it: it never creates or reconfigures the stream, since a
// second owner with different retention settings would fight core over it.
const StreamName = "BOOTH_EVENTS"

// DefaultConsumerName is the durable consumer's name. It must be stable across restarts and
// shared by every replica: JetStream remembers the delivery position under this name (which
// is how a catalog that was down still gets the events published meanwhile, ADR 0021's whole
// reason for JetStream), and replicas pulling from one durable consumer share the work.
const DefaultConsumerName = "booth-catalog-dashboards"

// SubscriberConfig configures a Subscriber. Only URL is required.
type SubscriberConfig struct {
	// URL is the NATS server, e.g. nats://booth-core-nats:4222.
	URL string
	// CredentialsFile is the path to the NATS ".creds" file booth-core provisions for this module
	// (ADR 0050: Secret "booth-event-bus-credentials", key "nats.creds"). Empty connects without
	// credentials, which only works against a bus with authentication switched off (local
	// development, or a test stand-in) — booth-core's bus refuses it.
	//
	// It is re-read on every (re)connect, never cached: core renews the credential in place
	// before it expires, and a long-running catalog has to pick the renewed one up.
	CredentialsFile string
	// StreamName defaults to StreamName; Consumer to DefaultConsumerName. Overridable so
	// tests can run several subscribers side by side.
	StreamName string
	Consumer   string

	// AckWait is how long JetStream waits for an ack before redelivering (default 30s).
	AckWait time.Duration
	// MaxDeliver bounds redelivery of one message (default 50). With the retry delay capped
	// at five minutes that tolerates an outage of several hours, while still ensuring a
	// message the catalog somehow can never process eventually stops being redelivered.
	MaxDeliver int
	// RetryDelay is how long to wait before redelivering a message that failed transiently,
	// given how many times it has been delivered so far. Default: exponential from 2s, capped
	// at 5 minutes.
	RetryDelay func(delivered int) time.Duration

	// ReconnectBase and ReconnectMax bound the wait between attempts to (re)establish the
	// subscription after a failure (defaults 1s and 30s). StreamPollInterval is how often to
	// look for the stream while core hasn't created it yet (default 2s).
	ReconnectBase      time.Duration
	ReconnectMax       time.Duration
	StreamPollInterval time.Duration

	// Logf receives operational messages (default: the standard logger, i.e. stdout —
	// ADR 0022, there is no logging API).
	Logf func(format string, args ...any)
}

func (c *SubscriberConfig) applyDefaults() {
	if c.StreamName == "" {
		c.StreamName = StreamName
	}
	if c.Consumer == "" {
		c.Consumer = DefaultConsumerName
	}
	if c.AckWait <= 0 {
		c.AckWait = 30 * time.Second
	}
	if c.MaxDeliver <= 0 {
		c.MaxDeliver = 50
	}
	if c.RetryDelay == nil {
		c.RetryDelay = defaultRetryDelay
	}
	if c.ReconnectBase <= 0 {
		c.ReconnectBase = time.Second
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = 30 * time.Second
	}
	if c.StreamPollInterval <= 0 {
		c.StreamPollInterval = 2 * time.Second
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
}

func defaultRetryDelay(delivered int) time.Duration {
	d := 2 * time.Second
	for i := 1; i < delivered && d < 5*time.Minute; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// State is where the subscription currently is.
type State string

const (
	// StateConnecting: dialing NATS (or re-dialing after a failure).
	StateConnecting State = "connecting"
	// StateWaitingForStream: connected, but booth-core hasn't created the BOOTH_EVENTS stream
	// yet. Expected briefly at install time; the subscriber keeps polling.
	StateWaitingForStream State = "waiting-for-stream"
	// StateSubscribed: consuming.
	StateSubscribed State = "subscribed"
	// StateError: the last attempt failed; retrying with backoff. Detail says why.
	StateError State = "error"
)

// Status is a snapshot of the subscription, for the health endpoint.
type Status struct {
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Subscriber is the JetStream transport: a durable pull consumer feeding a Processor.
type Subscriber struct {
	cfg  SubscriberConfig
	proc *Processor

	mu     sync.RWMutex
	status Status
}

func NewSubscriber(cfg SubscriberConfig, proc *Processor) *Subscriber {
	cfg.applyDefaults()
	return &Subscriber{cfg: cfg, proc: proc, status: Status{State: StateConnecting}}
}

// Status reports the subscription's current state.
func (s *Subscriber) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Subscriber) setStatus(state State, detail string) {
	s.mu.Lock()
	s.status = Status{State: state, Detail: detail}
	s.mu.Unlock()
}

// Run maintains the subscription until ctx is cancelled: it connects, waits for the stream,
// consumes, and on any failure tears down and starts over with exponential backoff. It
// never returns an error — a NATS outage must not take the catalog's HTTP API down with it —
// and blocks, so call it in its own goroutine.
func (s *Subscriber) Run(ctx context.Context) {
	wait := s.cfg.ReconnectBase
	for ctx.Err() == nil {
		started := time.Now()
		err := s.session(ctx)
		if ctx.Err() != nil {
			return
		}
		s.setStatus(StateError, err.Error())
		s.cfg.Logf("events: subscription failed, retrying in %s: %v", wait, err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		// A session that stayed up a while wasn't failing fast: start the backoff over.
		if time.Since(started) > 2*s.cfg.ReconnectMax {
			wait = s.cfg.ReconnectBase
		} else if wait *= 2; wait > s.cfg.ReconnectMax {
			wait = s.cfg.ReconnectMax
		}
	}
}

// session runs one connection's lifetime. It returns nil only when ctx is cancelled.
func (s *Subscriber) session(ctx context.Context) error {
	s.setStatus(StateConnecting, "")
	// Closed is signalled when the client library gives up on the connection for good — most
	// importantly when the credential has expired and the server keeps refusing it. Without
	// this the session would sit on a dead connection forever, still reporting "subscribed".
	// Ending the session sends Run's loop back to connect again, which re-reads the
	// credentials file and so picks up a renewed credential.
	closed := make(chan struct{}, 1)
	opts := []nats.Option{
		nats.Name("booth-catalog"),
		// Once connected, let the client library ride out a broker restart itself; the outer
		// loop handles failing to connect in the first place, the connection being closed for
		// good, and the stream or consumer disappearing.
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.Timeout(5 * time.Second),
		nats.ClosedHandler(func(*nats.Conn) {
			select {
			case closed <- struct{}{}:
			default:
			}
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				s.cfg.Logf("events: disconnected from NATS: %v", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) { s.cfg.Logf("events: reconnected to NATS at %s", c.ConnectedUrl()) }),
		// The server reports a denied publish or subscribe asynchronously, as a "permissions
		// violation" — the one signal that this module's declared events (its manifest) don't
		// cover something it tried to do. Without logging it, that failure is just a timeout.
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			s.cfg.Logf("events: NATS error: %v", err)
		}),
	}
	if s.cfg.CredentialsFile != "" {
		opts = append(opts, nats.UserCredentials(s.cfg.CredentialsFile))
	}
	nc, err := nats.Connect(s.cfg.URL, opts...)
	if err != nil {
		return fmt.Errorf("connecting to NATS at %s: %w", s.cfg.URL, err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("creating JetStream context: %w", err)
	}

	stream, err := s.waitForStream(ctx, js)
	if err != nil {
		return err
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       s.cfg.Consumer,
		FilterSubject: SubjectFilter,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       s.cfg.AckWait,
		MaxDeliver:    s.cfg.MaxDeliver,
		// DeliverAll on first creation: a catalog installed after dashboards already exist
		// picks up whatever the stream still retains, instead of starting blind.
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("creating consumer %s on %s: %w", s.cfg.Consumer, s.cfg.StreamName, err)
	}

	restart := make(chan error, 1)
	cc, err := consumer.Consume(
		func(msg jetstream.Msg) { s.handle(ctx, msg) },
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			s.cfg.Logf("events: consume error: %v", err)
			// A deleted consumer or stream (someone recreated it) won't heal on its own:
			// rebuild the whole session. Other errors (a heartbeat missed during a broker
			// restart) recover by themselves once the connection does.
			if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
				select {
				case restart <- err:
				default:
				}
			}
		}),
	)
	if err != nil {
		return fmt.Errorf("starting consume loop: %w", err)
	}
	defer cc.Stop()

	s.setStatus(StateSubscribed, "")
	s.cfg.Logf("events: subscribed to %s on stream %s as durable consumer %q", SubjectFilter, s.cfg.StreamName, s.cfg.Consumer)

	select {
	case <-ctx.Done():
		return nil
	case err := <-restart:
		return fmt.Errorf("consumer lost: %w", err)
	case <-closed:
		return errors.New("NATS connection closed (an expired or rejected credential, or the server went away for good)")
	}
}

// waitForStream polls until booth-core has created the stream. At install time the catalog
// may well start before core has finished bringing up its event bus.
func (s *Subscriber) waitForStream(ctx context.Context, js jetstream.JetStream) (jetstream.Stream, error) {
	for {
		stream, err := js.Stream(ctx, s.cfg.StreamName)
		if err == nil {
			return stream, nil
		}
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, fmt.Errorf("looking up stream %s: %w", s.cfg.StreamName, err)
		}
		s.setStatus(StateWaitingForStream, fmt.Sprintf("stream %s does not exist yet (booth-core creates it)", s.cfg.StreamName))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.cfg.StreamPollInterval):
		}
	}
}

// handle runs one message through the Processor and settles it with JetStream by the verdict.
func (s *Subscriber) handle(ctx context.Context, msg jetstream.Msg) {
	res := s.proc.Handle(ctx, msg.Subject(), msg.Data())
	switch res.Action {
	case Ack:
		if res.Reason != "" {
			s.cfg.Logf("events: %s: %s", msg.Subject(), res.Reason)
		}
		if err := msg.Ack(); err != nil {
			s.cfg.Logf("events: acking %s: %v", msg.Subject(), err) // redelivered; applying it again is idempotent
		}
	case Drop:
		// Term stops redelivery of a message that can never succeed. Log enough to find the
		// publisher at fault: the subject names the workspace and event type.
		s.cfg.Logf("events: dropping %s: %s", msg.Subject(), res.Reason)
		if err := msg.Term(); err != nil {
			s.cfg.Logf("events: terminating %s: %v", msg.Subject(), err)
		}
	case Retry:
		delivered := 1
		if md, err := msg.Metadata(); err == nil {
			delivered = int(md.NumDelivered)
		}
		delay := s.cfg.RetryDelay(delivered)
		s.cfg.Logf("events: %s failed transiently (delivery %d), retrying in %s: %s", msg.Subject(), delivered, delay, res.Reason)
		if err := msg.NakWithDelay(delay); err != nil {
			s.cfg.Logf("events: nacking %s: %v", msg.Subject(), err)
		}
	}
}
