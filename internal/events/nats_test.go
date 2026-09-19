package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/projectbooth/booth-catalog/internal/dashboards"
)

// These tests run a real nats-server with JetStream in-process: what is being verified —
// durability across restarts, acking, terminating a poison message, redelivery after a
// transient failure — are properties of JetStream itself, so a fake broker would prove
// nothing. It needs no container, so the tests run on every push with no setup.

func startServer(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server did not start")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// createStream makes the stream exactly as booth-core does (ADR 0026): BOOTH_EVENTS over
// booth.>.
func createStream(t *testing.T, url string) jetstream.JetStream {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name: StreamName, Subjects: []string{"booth.>"}, Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatal(err)
	}
	return js
}

func publish(t *testing.T, js jetstream.JetStream, subj string, payload []byte) {
	t.Helper()
	if _, err := js.Publish(context.Background(), subj, payload); err != nil {
		t.Fatalf("publishing to %s: %v", subj, err)
	}
}

func publishDashboard(t *testing.T, js jetstream.JetStream, ws, eventType string, at time.Time, id, name string) {
	t.Helper()
	data := any(dashboardData(id, name, nil))
	if eventType == EventDashboardDeleted {
		data = map[string]any{"dashboardId": id}
	}
	publish(t, js, subject(ws, eventType), envelope(ws, eventType, "superset", at, data))
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// countingCatalog wraps a real dashboard service, counting the calls that reach it and
// optionally failing the first few.
type countingCatalog struct {
	inner    *dashboards.Service
	failures atomic.Int32 // how many upcoming Apply calls should fail transiently
	applies  atomic.Int32
	removes  atomic.Int32
}

func (c *countingCatalog) Apply(ctx context.Context, u dashboards.Upsert) (bool, error) {
	c.applies.Add(1)
	if c.failures.Add(-1) >= 0 {
		return false, errors.New("database unavailable")
	}
	return c.inner.Apply(ctx, u)
}

func (c *countingCatalog) Remove(ctx context.Context, ws, module, id string, at time.Time) (bool, error) {
	c.removes.Add(1)
	return c.inner.Remove(ctx, ws, module, id, at)
}

type harness struct {
	url     string
	js      jetstream.JetStream
	svc     *dashboards.Service
	catalog *countingCatalog
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	url := startServer(t)
	svc := newService()
	return &harness{url: url, js: createStream(t, url), svc: svc, catalog: &countingCatalog{inner: svc}}
}

// start runs a Subscriber until the returned stop function is called (or the test ends).
func (h *harness) start(t *testing.T, mutate ...func(*SubscriberConfig)) (*Subscriber, func()) {
	t.Helper()
	cfg := SubscriberConfig{
		URL: h.url, AckWait: 2 * time.Second, ReconnectBase: 50 * time.Millisecond, ReconnectMax: 200 * time.Millisecond,
		StreamPollInterval: 50 * time.Millisecond, RetryDelay: func(int) time.Duration { return 20 * time.Millisecond },
		Logf: t.Logf,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	sub := NewSubscriber(cfg, NewProcessor(h.catalog))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sub.Run(ctx) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Subscriber.Run did not return after its context was cancelled")
		}
	}
	t.Cleanup(stop)
	return sub, stop
}

func (h *harness) names(ws string) []string {
	page, _ := h.svc.List(context.Background(), ws, dashboards.ListFilter{})
	out := make([]string, len(page.Items))
	for i, d := range page.Items {
		out[i] = d.Name
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestSubscriber_EndToEndAcrossWorkspaces(t *testing.T) {
	h := newHarness(t)
	sub, _ := h.start(t)
	eventually(t, "the subscription to be established", func() bool { return sub.Status().State == StateSubscribed })

	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0, "1", "Acme Revenue")
	publishDashboard(t, h.js, "globex", EventDashboardCreated, t0, "1", "Globex Revenue")
	eventually(t, "both workspaces' dashboards to be indexed", func() bool {
		return contains(h.names("acme"), "Acme Revenue") && contains(h.names("globex"), "Globex Revenue")
	})

	publishDashboard(t, h.js, "acme", EventDashboardUpdated, t0.Add(time.Minute), "1", "Acme Revenue v2")
	eventually(t, "the update", func() bool { return contains(h.names("acme"), "Acme Revenue v2") })

	publishDashboard(t, h.js, "acme", EventDashboardDeleted, t0.Add(2*time.Minute), "1", "")
	eventually(t, "the delete", func() bool { return len(h.names("acme")) == 0 })
	if got := h.names("globex"); len(got) != 1 {
		t.Errorf("deleting in acme touched globex: %v", got)
	}
}

// Events other modules publish on the same stream are none of this consumer's business: the
// filter keeps them from ever being delivered.
func TestSubscriber_OnlyReceivesDashboardEvents(t *testing.T) {
	h := newHarness(t)
	sub, _ := h.start(t)
	eventually(t, "subscribed", func() bool { return sub.Status().State == StateSubscribed })

	publish(t, h.js, "booth.acme.dataset.written", envelope("acme", "dataset.written", "spark", t0, map[string]any{"x": 1}))
	publish(t, h.js, "booth.acme.pipeline.run.finished", envelope("acme", "pipeline.run.finished", "pipeline", t0, map[string]any{}))
	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0, "1", "Only me")
	eventually(t, "the dashboard event", func() bool { return contains(h.names("acme"), "Only me") })

	cons, err := h.js.Consumer(context.Background(), StreamName, DefaultConsumerName)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := cons.Info(context.Background())
	if info.Delivered.Consumer != 1 {
		t.Errorf("consumer was delivered %d messages, want exactly the 1 dashboard event", info.Delivered.Consumer)
	}
}

// One malformed message must not wedge the consumer or be redelivered forever.
func TestSubscriber_APoisonMessageIsTerminatedNotRetried(t *testing.T) {
	h := newHarness(t)
	sub, _ := h.start(t)
	eventually(t, "subscribed", func() bool { return sub.Status().State == StateSubscribed })

	publish(t, h.js, subject("acme", EventDashboardCreated), []byte("{this is not json"))
	publish(t, h.js, subject("acme", EventDashboardCreated), envelope("globex", EventDashboardCreated, "superset", t0, dashboardData("1", "Wrong workspace", nil)))
	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0, "2", "After the poison")
	eventually(t, "the good event after the bad ones", func() bool { return contains(h.names("acme"), "After the poison") })

	cons, _ := h.js.Consumer(context.Background(), StreamName, DefaultConsumerName)
	eventually(t, "every message to be settled", func() bool {
		info, _ := cons.Info(context.Background())
		return info.NumAckPending == 0 && info.NumPending == 0
	})
	info, _ := cons.Info(context.Background())
	if info.NumRedelivered != 0 {
		t.Errorf("%d message(s) were redelivered; malformed events must be terminated on first sight", info.NumRedelivered)
	}
	if got := h.catalog.applies.Load(); got != 1 {
		t.Errorf("the catalog was called %d times, want 1 (only the well-formed event reaches it)", got)
	}
}

// A database blip must not lose the event: it is redelivered until it applies.
func TestSubscriber_RetriesTransientFailuresUntilTheyApply(t *testing.T) {
	h := newHarness(t)
	h.catalog.failures.Store(2)
	sub, _ := h.start(t)
	eventually(t, "subscribed", func() bool { return sub.Status().State == StateSubscribed })

	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0, "1", "Survives an outage")
	eventually(t, "the event to apply after two failures", func() bool { return contains(h.names("acme"), "Survives an outage") })

	if got := h.catalog.applies.Load(); got != 3 {
		t.Errorf("Apply was called %d times, want 3 (two failures, then success)", got)
	}
}

// The reason JetStream was chosen (ADR 0021): a catalog that is down — or just restarting —
// still receives what was published while it was away, and does not re-process what it
// already handled.
func TestSubscriber_DurableConsumerCatchesUpAfterDowntime(t *testing.T) {
	h := newHarness(t)

	_, stop := h.start(t)
	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0, "1", "Before")
	eventually(t, "the first event", func() bool { return contains(h.names("acme"), "Before") })
	stop()

	// Published while no catalog is running.
	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0.Add(time.Second), "2", "While down A")
	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0.Add(2*time.Second), "3", "While down B")

	h.start(t)
	eventually(t, "the missed events to arrive", func() bool {
		n := h.names("acme")
		return contains(n, "While down A") && contains(n, "While down B")
	})
	// Give any wrongly redelivered message time to show up before asserting it didn't.
	time.Sleep(200 * time.Millisecond)
	if got := h.catalog.applies.Load(); got != 3 {
		t.Errorf("Apply was called %d times, want 3: the event handled before the restart must not be delivered again", got)
	}
}

// At install time the catalog can come up before booth-core has created the stream.
func TestSubscriber_WaitsForTheStreamToExist(t *testing.T) {
	url := startServer(t)
	svc := newService()
	h := &harness{url: url, svc: svc, catalog: &countingCatalog{inner: svc}}
	sub, _ := h.start(t)

	eventually(t, "the subscriber to report it is waiting", func() bool { return sub.Status().State == StateWaitingForStream })
	if d := sub.Status().Detail; d == "" {
		t.Error("waiting status has no explanation")
	}

	h.js = createStream(t, url) // booth-core finishes starting up
	eventually(t, "the subscriber to attach once the stream appears", func() bool { return sub.Status().State == StateSubscribed })

	publishDashboard(t, h.js, "acme", EventDashboardCreated, t0, "1", "Late stream")
	eventually(t, "an event on the late stream", func() bool { return contains(h.names("acme"), "Late stream") })
}

// A NATS outage must never take the catalog down: Run keeps retrying and reports why.
func TestSubscriber_UnreachableServerIsRetriedNotFatal(t *testing.T) {
	svc := newService()
	sub := NewSubscriber(SubscriberConfig{
		URL: "nats://127.0.0.1:1", ReconnectBase: 20 * time.Millisecond, ReconnectMax: 50 * time.Millisecond, Logf: t.Logf,
	}, NewProcessor(svc))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sub.Run(ctx) }()

	eventually(t, "an error status", func() bool { return sub.Status().State == StateError })
	if sub.Status().Detail == "" {
		t.Error("error status has no detail")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestSubscriber_RecoversWhenTheConsumerIsDeletedUnderIt(t *testing.T) {
	h := newHarness(t)
	sub, _ := h.start(t)
	eventually(t, "subscribed", func() bool { return sub.Status().State == StateSubscribed })

	if err := h.js.DeleteConsumer(context.Background(), StreamName, DefaultConsumerName); err != nil {
		t.Fatal(err)
	}
	// The session rebuilds itself: a fresh durable consumer, and events flow again.
	var mu sync.Mutex
	seq := 0
	eventually(t, "events to flow again after the consumer was deleted", func() bool {
		mu.Lock()
		defer mu.Unlock()
		seq++
		publishDashboard(t, h.js, "acme", EventDashboardCreated, t0.Add(time.Duration(seq)*time.Second), "9", "Recovered")
		return contains(h.names("acme"), "Recovered")
	})
}

func TestDefaultRetryDelay(t *testing.T) {
	want := map[int]time.Duration{0: 2 * time.Second, 1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second, 8: 256 * time.Second, 9: 5 * time.Minute, 50: 5 * time.Minute}
	for delivered, d := range want {
		if got := defaultRetryDelay(delivered); got != d {
			t.Errorf("defaultRetryDelay(%d) = %s, want %s", delivered, got, d)
		}
	}
}

func TestEnvelopeRoundTripsThroughTheWire(t *testing.T) {
	// The shape booth-core publishes (eventbus.Envelope) must be what Processor reads.
	core := struct {
		Workspace   string         `json:"workspace"`
		EventType   string         `json:"eventType"`
		PublishedAt time.Time      `json:"publishedAt"`
		PublishedBy string         `json:"publishedBy"`
		Data        map[string]any `json:"data"`
	}{"acme", EventDashboardCreated, t0, "superset", dashboardData("1", "From core's envelope type", nil)}
	payload, err := json.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	svc := newService()
	if res := NewProcessor(svc).Handle(context.Background(), subject("acme", EventDashboardCreated), payload); !res.Applied {
		t.Errorf("result = %+v", res)
	}
}
