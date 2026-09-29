package events

import (
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/data"
)

func newDataService() *data.Service { return data.NewService(data.NewMemoryStore()) }

func tableSubject(workspace, eventType string) string { return "booth." + workspace + "." + eventType }

func tableData(uuid, ns, name string, extra map[string]any) map[string]any {
	m := map[string]any{
		"namespace": ns, "name": name, "tableUuid": uuid,
		"location": map[string]any{"backendId": "lake", "path": "warehouse/" + ns + "/" + name},
		"schema":   []map[string]any{{"name": "id", "type": "long"}},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func listTables(t *testing.T, svc *data.Service, ws string) []data.Dataset {
	t.Helper()
	page, err := svc.List(ctx, ws, data.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestTableHandle_CreatedUpdatedDeletedLifecycle(t *testing.T) {
	svc := newDataService()
	p := NewTableProcessor(svc)
	snap := int64(5)

	created := p.Handle(ctx, tableSubject("acme", EventTableCreated),
		envelope("acme", EventTableCreated, "lakehouse", t0, tableData("tbl-1", "sales", "orders", map[string]any{"currentSnapshotId": snap})))
	if created != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("created = %+v", created)
	}
	ds := listTables(t, svc, "acme")
	if len(ds) != 1 || ds[0].Name != "sales.orders" || ds[0].Format != data.FormatIceberg || ds[0].Table == nil ||
		ds[0].Table.UUID != "tbl-1" || ds[0].Table.CurrentSnapshotID == nil || *ds[0].Table.CurrentSnapshotID != 5 {
		t.Fatalf("indexed = %+v", ds)
	}

	// An update can rename (namespace.name changes) without losing identity: it's still the
	// same row, addressed by (module, UUID), not by name.
	updated := p.Handle(ctx, tableSubject("acme", EventTableUpdated),
		envelope("acme", EventTableUpdated, "lakehouse", t0.Add(time.Minute), tableData("tbl-1", "sales", "orders_v2", nil)))
	if updated != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("updated = %+v", updated)
	}
	if ds = listTables(t, svc, "acme"); len(ds) != 1 || ds[0].Name != "sales.orders_v2" {
		t.Errorf("after update: %+v", ds)
	}

	deleted := p.Handle(ctx, tableSubject("acme", EventTableDeleted),
		envelope("acme", EventTableDeleted, "lakehouse", t0.Add(2*time.Minute), map[string]any{"tableUuid": "tbl-1"}))
	if deleted != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("deleted = %+v", deleted)
	}
	if ds = listTables(t, svc, "acme"); len(ds) != 0 {
		t.Errorf("after delete: %+v", ds)
	}
}

// A catalog installed after the table already existed must not lose it: updated is a
// full-state upsert, exactly like DashboardData's equivalent guarantee.
func TestTableHandle_UpdatedForAnUnseenTableCreatesIt(t *testing.T) {
	svc := newDataService()
	res := NewTableProcessor(svc).Handle(ctx, tableSubject("acme", EventTableUpdated),
		envelope("acme", EventTableUpdated, "lakehouse", t0, tableData("tbl-9", "eng", "seen_first_as_update", nil)))
	if !res.Applied || res.Action != Ack {
		t.Fatalf("result = %+v", res)
	}
	if ds := listTables(t, svc, "acme"); len(ds) != 1 || ds[0].Table.UUID != "tbl-9" {
		t.Errorf("indexed = %+v", ds)
	}
}

func TestTableHandle_StaleAndRedeliveredEvents(t *testing.T) {
	svc := newDataService()
	p := NewTableProcessor(svc)
	send := func(at time.Time, name string) Result {
		return p.Handle(ctx, tableSubject("acme", EventTableUpdated),
			envelope("acme", EventTableUpdated, "lakehouse", at, tableData("tbl-1", "sales", name, nil)))
	}

	send(t0.Add(10*time.Second), "newer")

	stale := send(t0, "older")
	if stale.Action != Ack || stale.Applied || !strings.Contains(stale.Reason, "stale") {
		t.Errorf("stale = %+v; want acked (nothing to retry) and not applied", stale)
	}
	if ds := listTables(t, svc, "acme"); ds[0].Name != "sales.newer" {
		t.Errorf("a stale event rolled the table back: %q", ds[0].Name)
	}

	if again := send(t0.Add(10*time.Second), "newer"); again.Action != Ack {
		t.Errorf("redelivery = %+v", again)
	}
}

func TestTableHandle_WorkspacesAreScopedBySubject(t *testing.T) {
	svc := newDataService()
	p := NewTableProcessor(svc)
	for _, ws := range []string{"acme", "globex"} {
		res := p.Handle(ctx, tableSubject(ws, EventTableCreated), envelope(ws, EventTableCreated, "lakehouse", t0, tableData("tbl-1", "sales", "orders", nil)))
		if !res.Applied {
			t.Fatalf("%s: %+v", ws, res)
		}
	}
	a, g := listTables(t, svc, "acme"), listTables(t, svc, "globex")
	if len(a) != 1 || len(g) != 1 {
		t.Errorf("acme=%+v globex=%+v; the same table UUID in two workspaces must be two datasets", a, g)
	}
}

// A table's computed name colliding with an unrelated, already-registered dataset can never
// succeed by retrying — it needs a human to rename one of them.
func TestTableHandle_DropsOnANameCollision(t *testing.T) {
	svc := newDataService()
	actor := asset.Actor{Subject: "sub-alice", DisplayName: "alice@example.com"}
	if _, err := svc.Create(ctx, "acme", actor, data.Input{Name: "sales.orders", Location: asset.Location{BackendID: "lake", Path: "x"}}); err != nil {
		t.Fatal(err)
	}
	res := NewTableProcessor(svc).Handle(ctx, tableSubject("acme", EventTableCreated),
		envelope("acme", EventTableCreated, "lakehouse", t0, tableData("tbl-1", "sales", "orders", nil)))
	if res.Action != Drop || res.Applied {
		t.Fatalf("name-colliding table = %+v, want Drop", res)
	}
}

// Anything that can never succeed is dropped — retrying it would loop forever. Mirrors
// TestHandle_DropsWhatCanNeverBeApplied for the table.* event family.
func TestTableHandle_DropsWhatCanNeverBeApplied(t *testing.T) {
	good := tableData("tbl-1", "sales", "orders", nil)
	cases := []struct {
		name    string
		subject string
		payload []byte
		reason  string
	}{
		{"not a table subject", "booth.acme.dashboard.created", envelope("acme", EventDashboardCreated, "x", t0, good), "not a table lifecycle event"},
		{"wrong number of subject tokens", "booth.acme.table.created.extra", envelope("acme", EventTableCreated, "lakehouse", t0, good), "not a table"},
		{"unknown table verb", "booth.acme.table.archived", envelope("acme", "table.archived", "lakehouse", t0, good), "not a table"},
		{"garbage payload", tableSubject("acme", EventTableCreated), []byte("not json"), "malformed event envelope"},
		{"empty payload", tableSubject("acme", EventTableCreated), nil, "malformed event envelope"},
		{"envelope names another workspace", tableSubject("acme", EventTableCreated), envelope("globex", EventTableCreated, "lakehouse", t0, good), "does not match subject workspace"},
		{"envelope names another event type", tableSubject("acme", EventTableCreated), envelope("acme", EventTableDeleted, "lakehouse", t0, good), "does not match subject event type"},
		{"missing namespace", tableSubject("acme", EventTableCreated), envelope("acme", EventTableCreated, "lakehouse", t0, tableData("tbl-1", "", "orders", nil)), "namespace"},
		{"missing name", tableSubject("acme", EventTableCreated), envelope("acme", EventTableCreated, "lakehouse", t0, tableData("tbl-1", "sales", "", nil)), "name"},
		{"missing table uuid", tableSubject("acme", EventTableCreated), envelope("acme", EventTableCreated, "lakehouse", t0, tableData("", "sales", "orders", nil)), "tableUuid"},
		{"missing publishedBy", tableSubject("acme", EventTableCreated), envelope("acme", EventTableCreated, "", t0, good), "publishedBy"},
		{"missing publishedAt", tableSubject("acme", EventTableCreated), []byte(`{"workspace":"acme","eventType":"table.created","publishedBy":"lakehouse","data":{"namespace":"sales","name":"orders","tableUuid":"tbl-1","location":{"backendId":"lake","path":"x"}}}`), "publishedAt"},
		{"delete without a uuid", tableSubject("acme", EventTableDeleted), envelope("acme", EventTableDeleted, "lakehouse", t0, map[string]any{}), "tableUuid"},
		{"invalid workspace slug", "booth.Not_A_Slug.table.created", envelope("Not_A_Slug", EventTableCreated, "lakehouse", t0, good), "invalid workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newDataService()
			res := NewTableProcessor(svc).Handle(ctx, tc.subject, tc.payload)
			if res.Action != Drop {
				t.Fatalf("action = %v, want Drop (reason %q)", res.Action, res.Reason)
			}
			if !strings.Contains(res.Reason, tc.reason) {
				t.Errorf("reason = %q, want it to mention %q", res.Reason, tc.reason)
			}
		})
	}
}
