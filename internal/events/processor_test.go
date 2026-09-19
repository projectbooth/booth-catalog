package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/dashboards"
)

var ctx = context.Background()

var t0 = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// noDatasets is a dataset catalog with nothing registered.
type noDatasets struct{}

func (noDatasets) ByID(context.Context, string, string) (dashboards.DatasetRef, bool, error) {
	return dashboards.DatasetRef{}, false, nil
}
func (noDatasets) ByLocation(context.Context, string, asset.Location) ([]dashboards.DatasetRef, error) {
	return nil, nil
}

func newService() *dashboards.Service {
	return dashboards.NewService(dashboards.NewMemoryStore(), noDatasets{})
}

// envelope builds a message payload the way a publisher would (ADR 0026's envelope).
func envelope(workspace, eventType, publishedBy string, at time.Time, data any) []byte {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	out, err := json.Marshal(map[string]any{
		"workspace": workspace, "eventType": eventType, "publishedAt": at.Format(time.RFC3339Nano),
		"publishedBy": publishedBy, "data": json.RawMessage(raw),
	})
	if err != nil {
		panic(err)
	}
	return out
}

func subject(workspace, eventType string) string { return "booth." + workspace + "." + eventType }

func dashboardData(id, name string, extra map[string]any) map[string]any {
	m := map[string]any{"dashboardId": id, "name": name, "owner": "alice@example.com", "path": "/superset/dashboard/" + id}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func list(t *testing.T, svc *dashboards.Service, ws string) []dashboards.Dashboard {
	t.Helper()
	page, err := svc.List(ctx, ws, dashboards.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestHandle_CreatedUpdatedDeletedLifecycle(t *testing.T) {
	svc := newService()
	p := NewProcessor(svc)

	created := p.Handle(ctx, subject("acme", EventDashboardCreated),
		envelope("acme", EventDashboardCreated, "superset", t0, dashboardData("42", "Revenue", map[string]any{
			"description":     "Quarterly revenue",
			"lineageComplete": true,
			"sources": []map[string]any{
				{"type": "dataset", "datasetId": "ds-1"},
				{"type": "location", "backendId": "lake", "path": "warehouse/orders/"},
				{"type": "external", "name": "public.orders", "system": "postgres"},
			},
		})))
	if created != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("created = %+v", created)
	}
	ds := list(t, svc, "acme")
	if len(ds) != 1 || ds[0].Name != "Revenue" || ds[0].SourceModule != "superset" || ds[0].ExternalID != "42" ||
		ds[0].Owner != "alice@example.com" || ds[0].Path != "/superset/dashboard/42" || ds[0].Description != "Quarterly revenue" {
		t.Fatalf("indexed = %+v", ds)
	}
	detail, err := svc.Get(ctx, "acme", ds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Lineage.Complete || len(detail.Lineage.Sources) != 3 || detail.Lineage.Sources[1].Path != "warehouse/orders" {
		t.Errorf("lineage = %+v (the publisher's path should be normalized)", detail.Lineage)
	}

	updated := p.Handle(ctx, subject("acme", EventDashboardUpdated),
		envelope("acme", EventDashboardUpdated, "superset", t0.Add(time.Minute), dashboardData("42", "Revenue (renamed)", nil)))
	if updated != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("updated = %+v", updated)
	}
	if ds = list(t, svc, "acme"); len(ds) != 1 || ds[0].Name != "Revenue (renamed)" {
		t.Errorf("after update: %+v", ds)
	}

	deleted := p.Handle(ctx, subject("acme", EventDashboardDeleted),
		envelope("acme", EventDashboardDeleted, "superset", t0.Add(2*time.Minute), map[string]any{"dashboardId": "42"}))
	if deleted != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("deleted = %+v", deleted)
	}
	if ds = list(t, svc, "acme"); len(ds) != 0 {
		t.Errorf("after delete: %+v", ds)
	}
}

// A publisher that missed sending `created` (or a catalog installed after the dashboard
// existed) must not lose the dashboard: updated is a full-state upsert.
func TestHandle_UpdatedForAnUnseenDashboardCreatesIt(t *testing.T) {
	svc := newService()
	res := NewProcessor(svc).Handle(ctx, subject("acme", EventDashboardUpdated),
		envelope("acme", EventDashboardUpdated, "metabase", t0, dashboardData("7", "Seen first as an update", nil)))
	if !res.Applied || res.Action != Ack {
		t.Fatalf("result = %+v", res)
	}
	if ds := list(t, svc, "acme"); len(ds) != 1 || ds[0].SourceModule != "metabase" {
		t.Errorf("indexed = %+v", ds)
	}
}

func TestHandle_StaleAndRedeliveredEvents(t *testing.T) {
	svc := newService()
	p := NewProcessor(svc)
	send := func(at time.Time, name string) Result {
		return p.Handle(ctx, subject("acme", EventDashboardUpdated),
			envelope("acme", EventDashboardUpdated, "superset", at, dashboardData("42", name, nil)))
	}

	send(t0.Add(10*time.Second), "Newer")

	stale := send(t0, "Older")
	if stale.Action != Ack || stale.Applied || !strings.Contains(stale.Reason, "stale") {
		t.Errorf("stale = %+v; want acked (nothing to retry) and not applied", stale)
	}
	if ds := list(t, svc, "acme"); ds[0].Name != "Newer" {
		t.Errorf("a stale event rolled the dashboard back: %q", ds[0].Name)
	}

	// Redelivery of the same event is safe.
	if again := send(t0.Add(10*time.Second), "Newer"); again.Action != Ack {
		t.Errorf("redelivery = %+v", again)
	}
}

func TestHandle_WorkspacesAreScopedBySubject(t *testing.T) {
	svc := newService()
	p := NewProcessor(svc)
	for _, ws := range []string{"acme", "globex"} {
		res := p.Handle(ctx, subject(ws, EventDashboardCreated), envelope(ws, EventDashboardCreated, "superset", t0, dashboardData("1", "Sales "+ws, nil)))
		if !res.Applied {
			t.Fatalf("%s: %+v", ws, res)
		}
	}
	if a, g := list(t, svc, "acme"), list(t, svc, "globex"); len(a) != 1 || a[0].Name != "Sales acme" || len(g) != 1 || g[0].Name != "Sales globex" {
		t.Errorf("acme=%+v globex=%+v; the same tool-local ID in two workspaces must be two dashboards", a, g)
	}
}

// Anything that can never succeed is dropped — retrying it would loop forever.
func TestHandle_DropsWhatCanNeverBeApplied(t *testing.T) {
	good := dashboardData("1", "Fine", nil)
	cases := []struct {
		name    string
		subject string
		payload []byte
		reason  string
	}{
		{"not a dashboard subject", "booth.acme.dataset.written", envelope("acme", "dataset.written", "x", t0, good), "not a dashboard lifecycle event"},
		{"wrong number of subject tokens", "booth.acme.dashboard.created.extra", envelope("acme", EventDashboardCreated, "superset", t0, good), "not a dashboard"},
		{"unknown dashboard verb", "booth.acme.dashboard.archived", envelope("acme", "dashboard.archived", "superset", t0, good), "not a dashboard"},
		{"garbage payload", subject("acme", EventDashboardCreated), []byte("not json"), "malformed event envelope"},
		{"empty payload", subject("acme", EventDashboardCreated), nil, "malformed event envelope"},
		{"data of the wrong shape", subject("acme", EventDashboardCreated), envelope("acme", EventDashboardCreated, "superset", t0, []int{1, 2}), "malformed dashboard.created data"},
		// The subject decides the workspace; a payload contradicting it is refused, not trusted.
		{"envelope names another workspace", subject("acme", EventDashboardCreated), envelope("globex", EventDashboardCreated, "superset", t0, good), "does not match subject workspace"},
		{"envelope names another event type", subject("acme", EventDashboardCreated), envelope("acme", EventDashboardDeleted, "superset", t0, good), "does not match subject event type"},
		{"missing dashboard id", subject("acme", EventDashboardCreated), envelope("acme", EventDashboardCreated, "superset", t0, dashboardData("", "X", nil)), "dashboardId"},
		{"missing name", subject("acme", EventDashboardCreated), envelope("acme", EventDashboardCreated, "superset", t0, dashboardData("1", "", nil)), "name"},
		{"missing publishedBy", subject("acme", EventDashboardCreated), envelope("acme", EventDashboardCreated, "", t0, good), "publishedBy"},
		{"missing publishedAt", subject("acme", EventDashboardCreated), []byte(`{"workspace":"acme","eventType":"dashboard.created","publishedBy":"superset","data":{"dashboardId":"1","name":"X"}}`), "publishedAt"},
		{"a path that leaves the shell", subject("acme", EventDashboardCreated), envelope("acme", EventDashboardCreated, "superset", t0, dashboardData("1", "X", map[string]any{"path": "https://evil.example"})), "path"},
		{"delete without an id", subject("acme", EventDashboardDeleted), envelope("acme", EventDashboardDeleted, "superset", t0, map[string]any{}), "dashboardId"},
		{"invalid workspace slug", "booth.Not_A_Slug.dashboard.created", envelope("Not_A_Slug", EventDashboardCreated, "superset", t0, good), "invalid workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService()
			res := NewProcessor(svc).Handle(ctx, tc.subject, tc.payload)
			if res.Action != Drop || res.Applied || !strings.Contains(res.Reason, tc.reason) {
				t.Errorf("result = %+v; want a drop mentioning %q", res, tc.reason)
			}
			if len(list(t, svc, "acme"))+len(list(t, svc, "globex")) != 0 {
				t.Error("a dropped event still changed the catalog")
			}
		})
	}
}

// A bad lineage entry must not cost the dashboard: it is indexed with what was usable and
// the lineage marked incomplete.
func TestHandle_BadSourcesDegradeLineageNotTheDashboard(t *testing.T) {
	svc := newService()
	res := NewProcessor(svc).Handle(ctx, subject("acme", EventDashboardCreated),
		envelope("acme", EventDashboardCreated, "superset", t0, dashboardData("1", "Mostly fine", map[string]any{
			"lineageComplete": true,
			"sources": []map[string]any{
				{"type": "dataset", "datasetId": "ds-1"},
				{"type": "location", "backendId": "lake", "path": "../escape"},
			},
		})))
	if res.Action != Ack || !res.Applied {
		t.Fatalf("result = %+v", res)
	}
	ds := list(t, svc, "acme")
	detail, _ := svc.Get(ctx, "acme", ds[0].ID)
	if len(detail.Lineage.Sources) != 1 || detail.Lineage.Complete {
		t.Errorf("lineage = %+v; want 1 source and not complete", detail.Lineage)
	}
}

func TestHandle_IgnoresUnknownFields(t *testing.T) {
	svc := newService()
	// A newer publisher adds fields this consumer has never heard of.
	res := NewProcessor(svc).Handle(ctx, subject("acme", EventDashboardCreated),
		envelope("acme", EventDashboardCreated, "superset", t0, dashboardData("1", "Forward compatible", map[string]any{
			"folder": "Finance", "charts": 12,
			"sources": []map[string]any{{"type": "external", "name": "t", "engine": "trino"}},
		})))
	if res.Action != Ack || !res.Applied {
		t.Errorf("result = %+v", res)
	}
}

// failingCatalog fails like an unavailable database.
type failingCatalog struct{ err error }

func (f failingCatalog) Apply(context.Context, dashboards.Upsert) (bool, error) { return false, f.err }
func (f failingCatalog) Remove(context.Context, string, string, string, time.Time) (bool, error) {
	return false, f.err
}

// An infrastructure failure says nothing is wrong with the event: it must be retried, not
// dropped, or an outage would silently lose every dashboard change made during it.
func TestHandle_RetriesTransientFailures(t *testing.T) {
	p := NewProcessor(failingCatalog{err: errors.New("connection refused")})
	for _, tc := range []struct{ eventType string }{{EventDashboardCreated}, {EventDashboardUpdated}, {EventDashboardDeleted}} {
		data := any(dashboardData("1", "X", nil))
		if tc.eventType == EventDashboardDeleted {
			data = map[string]any{"dashboardId": "1"}
		}
		res := p.Handle(ctx, subject("acme", tc.eventType), envelope("acme", tc.eventType, "superset", t0, data))
		if res.Action != Retry || res.Applied || !strings.Contains(res.Reason, "connection refused") {
			t.Errorf("%s: result = %+v; want a retry", tc.eventType, res)
		}
	}
}

func TestActionString(t *testing.T) {
	if Ack.String() != "ack" || Retry.String() != "retry" || Drop.String() != "drop" {
		t.Errorf("%v %v %v", Ack, Retry, Drop)
	}
}
