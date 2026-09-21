package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/projectbooth/booth-catalog/internal/dashboards"
)

// These tests run the subscriber against a NATS server in JWT operator/account mode, the way
// booth-core's bus runs since ADR 0049/0050: every connection needs a signed credential, and the
// credential's embedded permissions are default-deny. That is the configuration in which this
// module's unauthenticated first build could not connect at all (booth-e2e's SKIPped
// smoke.7-dashboard-event), and the one the unauthenticated tests in nats_test.go can never
// exercise — they would pass just as happily against a bus this module is locked out of.

// moduleGrants mirrors what booth-core's internal/natsauth.GrantsFor derives from a manifest
// declaring `events: {subscribe: ["dashboard.*"]}` — the subject allow-lists baked into the
// credential core mints for this module. It is a COPY, so it can drift from core's: if core's
// grants for a subscriber ever change, this must follow, and booth-e2e is the check that they
// still agree in a real deployment.
func moduleGrants() (publish, subscribe []string) {
	const stream = StreamName
	publish = []string{
		"$JS.API.CONSUMER.CREATE." + stream + ".>",
		"$JS.API.CONSUMER.DURABLE.CREATE." + stream + ".>",
		"$JS.API.CONSUMER.INFO." + stream + ".>",
		"$JS.API.CONSUMER.MSG.NEXT." + stream + ".>",
		"$JS.ACK." + stream + ".>",
		"$JS.FC." + stream + ".>",
		"$JS.API.INFO",
		"$JS.API.STREAM.INFO." + stream,
	}
	return publish, []string{"_INBOX.>"}
}

type jwtBus struct {
	url string
	acc nkeys.KeyPair
	dir string
}

// startJWTServer runs an in-process nats-server with JetStream in operator mode: one operator,
// one account (as in ADR 0050's shared BOOTH account) with JetStream enabled, and a system account.
func startJWTServer(t *testing.T) *jwtBus {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	op, err := nkeys.CreateOperator()
	must(err)
	opPub, _ := op.PublicKey()
	sys, err := nkeys.CreateAccount()
	must(err)
	sysPub, _ := sys.PublicKey()
	acc, err := nkeys.CreateAccount()
	must(err)
	accPub, _ := acc.PublicKey()

	opClaims := jwt.NewOperatorClaims(opPub)
	opClaims.Name = "booth-test-operator"
	opClaims.SystemAccount = sysPub

	sysJWT, err := jwt.NewAccountClaims(sysPub).Encode(op)
	must(err)
	accClaims := jwt.NewAccountClaims(accPub)
	accClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
	accJWT, err := accClaims.Encode(op)
	must(err)

	resolver := &natsserver.MemAccResolver{}
	must(resolver.Store(sysPub, sysJWT))
	must(resolver.Store(accPub, accJWT))

	srv, err := natsserver.NewServer(&natsserver.Options{
		Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
		TrustedOperators: []*jwt.OperatorClaims{opClaims}, AccountResolver: resolver, SystemAccount: sysPub,
	})
	must(err)
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server (operator mode) did not start")
	}
	t.Cleanup(srv.Shutdown)
	return &jwtBus{url: srv.ClientURL(), acc: acc, dir: t.TempDir()}
}

// creds mints a user credential in the account with the given permissions, writes it as a
// standard ".creds" file (what nats.UserCredentials reads, and what booth-core puts under the
// Secret's "nats.creds" key), and returns the path. expires of 0 means it doesn't expire.
func (b *jwtBus) creds(t *testing.T, name string, publish, subscribe []string, expires time.Duration) string {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := kp.PublicKey()
	seed, _ := kp.Seed()
	uc := jwt.NewUserClaims(pub)
	uc.Name = name
	uc.Pub.Allow.Add(publish...)
	uc.Sub.Allow.Add(subscribe...)
	if expires > 0 {
		uc.Expires = time.Now().Add(expires).Unix()
	}
	token, err := uc.Encode(b.acc)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := jwt.FormatUserConfig(token, seed)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.dir, name+".creds")
	writeFileAtomically(t, path, creds)
	return path
}

// writeFileAtomically replaces path in one step, as the kubelet does when it updates a mounted
// Secret: a reader sees the old file or the new one, never a half-written one.
func writeFileAtomically(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// coreConn connects as booth-core would: it owns the stream, so it may do anything.
func (b *jwtBus) coreConn(t *testing.T) jetstream.JetStream {
	t.Helper()
	path := b.creds(t, "core", []string{">"}, []string{">"}, 0)
	nc, err := nats.Connect(b.url, nats.UserCredentials(path))
	if err != nil {
		t.Fatalf("connecting as core: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name: StreamName, Subjects: []string{"booth.>"}, Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatalf("core creating the stream: %v", err)
	}
	return js
}

// logRecorder is a Logf that keeps what was logged, so a test can assert on it (a permissions
// violation is otherwise only visible as a hang).
type logRecorder struct {
	t  *testing.T
	mu sync.Mutex
	b  strings.Builder
}

func (l *logRecorder) Logf(format string, args ...any) {
	l.t.Logf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(strings.ToLower(strings.TrimSpace(fmt.Sprintf(format, args...))) + "\n")
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startCatalog(t *testing.T, url string, mutate func(*SubscriberConfig)) (*Subscriber, *dashboards.Service, *logRecorder) {
	t.Helper()
	svc := newService()
	rec := &logRecorder{t: t}
	cfg := SubscriberConfig{
		URL: url, AckWait: 2 * time.Second, ReconnectBase: 50 * time.Millisecond, ReconnectMax: 200 * time.Millisecond,
		StreamPollInterval: 50 * time.Millisecond, RetryDelay: func(int) time.Duration { return 20 * time.Millisecond }, Logf: rec.Logf,
	}
	mutate(&cfg)
	sub := NewSubscriber(cfg, NewProcessor(svc))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sub.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Subscriber.Run did not return after cancellation")
		}
	})
	return sub, svc, rec
}

func indexed(svc *dashboards.Service) []string {
	page, _ := svc.List(context.Background(), "acme", dashboards.ListFilter{})
	out := make([]string, len(page.Items))
	for i, d := range page.Items {
		out[i] = d.Name
	}
	return out
}

func TestSubscriberWithModuleScopedCredentials(t *testing.T) {
	bus := startJWTServer(t)
	core := bus.coreConn(t)
	pub, subj := moduleGrants()
	creds := bus.creds(t, "catalog", pub, subj, 0)

	sub, svc, rec := startCatalog(t, bus.url, func(c *SubscriberConfig) { c.CredentialsFile = creds })
	eventually(t, "the subscription to be established under scoped credentials", func() bool { return sub.Status().State == StateSubscribed })

	publishDashboard(t, core, "acme", EventDashboardCreated, t0, "1", "Scoped and working")
	eventually(t, "an event to arrive through the restricted consumer", func() bool { return contains(indexed(svc), "Scoped and working") })
	publishDashboard(t, core, "acme", EventDashboardUpdated, t0.Add(time.Minute), "1", "Renamed")
	eventually(t, "an update", func() bool { return contains(indexed(svc), "Renamed") })

	// A permissions violation is what a grant that is too narrow looks like from here; the
	// server reports it asynchronously and the operation just times out. Fail on the cause.
	if log := rec.String(); strings.Contains(log, "permissions violation") {
		t.Errorf("the subscriber hit a NATS permissions violation under the grants core derives:\n%s", log)
	}
}

// The other half of the test above: the harness really does enforce the module's grants. Without
// this, "works under scoped credentials" could just mean "the harness allows everything".
func TestModuleCredentialsCannotPublishEvents(t *testing.T) {
	bus := startJWTServer(t)
	bus.coreConn(t)
	pub, subj := moduleGrants()
	creds := bus.creds(t, "catalog", pub, subj, 0)

	nc, err := nats.Connect(bus.url, nats.UserCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// This module declared only `subscribe`: it may not forge a dashboard event into any workspace.
	if _, err := js.Publish(ctx, "booth.acme.dashboard.created", []byte("{}")); err == nil {
		t.Fatal("a subscribe-only credential was allowed to publish a dashboard event")
	}
	// Nor manage the shared stream (core alone may).
	if err := js.DeleteStream(ctx, StreamName); err == nil {
		t.Fatal("a module credential was allowed to delete the shared stream")
	}
}

// booth-core's bus refuses a connection with no credential at all. The catalog must say so, keep
// trying, and never claim to be subscribed — the silent version of this is what booth-e2e hit.
func TestSubscriberWithoutCredentialsIsRefusedAndSaysSo(t *testing.T) {
	bus := startJWTServer(t)
	core := bus.coreConn(t)

	sub, _, rec := startCatalog(t, bus.url, func(*SubscriberConfig) {})
	eventually(t, "an error status", func() bool { return sub.Status().State == StateError })
	if strings.Contains(rec.String(), "subscribed to") {
		t.Error("claimed to be subscribed without credentials")
	}

	// A credentials file that doesn't exist yet (core hasn't written the Secret) is the same
	// failure, retried until it appears — not a crash.
	missing := filepath.Join(bus.dir, "not-yet.creds")
	sub2, svc2, _ := startCatalog(t, bus.url, func(c *SubscriberConfig) { c.CredentialsFile = missing })
	eventually(t, "an error while the Secret is missing", func() bool { return sub2.Status().State == StateError })

	pub, subj := moduleGrants()
	writeFileAtomically(t, missing, mustRead(t, bus.creds(t, "catalog-late", pub, subj, 0)))
	eventually(t, "the subscription to attach once the credentials file appears", func() bool { return sub2.Status().State == StateSubscribed })
	publishDashboard(t, core, "acme", EventDashboardCreated, t0, "1", "After the Secret appeared")
	eventually(t, "an event after the late credentials", func() bool { return contains(indexed(svc2), "After the Secret appeared") })
}

// Credentials last 90 days and booth-core renews them in place. A catalog that outlives its
// first credential has to keep working: the file is re-read when the connection is re-made, so
// the renewed credential is picked up with no restart.
func TestSubscriberSurvivesItsCredentialExpiringWhenRenewed(t *testing.T) {
	bus := startJWTServer(t)
	core := bus.coreConn(t)
	pub, subj := moduleGrants()
	creds := bus.creds(t, "catalog", pub, subj, 3*time.Second)

	sub, svc, rec := startCatalog(t, bus.url, func(c *SubscriberConfig) { c.CredentialsFile = creds })
	eventually(t, "subscribed on the short-lived credential", func() bool { return sub.Status().State == StateSubscribed })
	publishDashboard(t, core, "acme", EventDashboardCreated, t0, "1", "Before expiry")
	eventually(t, "an event before expiry", func() bool { return contains(indexed(svc), "Before expiry") })

	// Core renews in place: same path, fresh credential (what the kubelet does to the mounted Secret).
	writeFileAtomically(t, creds, mustRead(t, bus.creds(t, "catalog-renewed", pub, subj, 0)))

	// Wait out the original credential, then prove events still flow.
	time.Sleep(5 * time.Second)
	seq := 0
	eventually(t, "events to keep flowing after the original credential expired", func() bool {
		seq++
		publishDashboard(t, core, "acme", EventDashboardCreated, t0.Add(time.Duration(seq)*time.Minute), "2", "After expiry")
		return contains(indexed(svc), "After expiry")
	})
	t.Logf("subscription log across the expiry:\n%s", rec.String())
}
