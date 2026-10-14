package events

import (
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/data"
)

func dbTableSubject(workspace, eventType string) string {
	return "booth." + workspace + "." + eventType
}

func dbTableData(schema, name string, extra map[string]any) map[string]any {
	m := map[string]any{
		"schema": schema, "name": name, "kind": "table",
		"columns": []map[string]any{{"name": "id", "type": "bigint", "nullable": false, "comment": "primary key"}},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func listPostgresTables(t *testing.T, svc *data.Service, ws string) []data.Dataset {
	t.Helper()
	page, err := svc.List(ctx, ws, data.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestDatabaseTableHandle_CreatedUpdatedDeletedLifecycle(t *testing.T) {
	svc := newDataService()
	p := NewDatabaseTableProcessor(svc)
	rows := int64(1696)

	created := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableCreated),
		envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "players", map[string]any{"approxRows": rows})))
	if created != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("created = %+v", created)
	}
	ds := listPostgresTables(t, svc, "acme")
	if len(ds) != 1 || ds[0].Name != "nfl_data.players" || ds[0].Format != data.FormatPostgres || ds[0].Source != data.SourceAuto ||
		ds[0].Kind != data.TableKindTable || ds[0].ApproxRows == nil || *ds[0].ApproxRows != 1696 ||
		ds[0].PostgresTable == nil || ds[0].PostgresTable.Schema != "nfl_data" || ds[0].PostgresTable.Name != "players" {
		t.Fatalf("indexed = %+v", ds)
	}
	if len(ds[0].Schema) != 1 || ds[0].Schema[0].Name != "id" || ds[0].Schema[0].Description != "primary key" {
		t.Errorf("columns = %+v", ds[0].Schema)
	}

	// An update refreshes columns/kind/approxRows in place — same identity, (workspace, schema,
	// name), never a UUID.
	updated := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableUpdated),
		envelope("acme", EventDatabaseTableUpdated, "database", t0.Add(time.Minute), dbTableData("nfl_data", "players", map[string]any{"kind": "view", "approxRows": nil})))
	if updated != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("updated = %+v", updated)
	}
	if ds = listPostgresTables(t, svc, "acme"); len(ds) != 1 || ds[0].Kind != data.TableKindView || ds[0].ApproxRows != nil {
		t.Errorf("after update: %+v", ds)
	}

	// A delete marks the row Missing — it stays listed, unlike an Iceberg tombstone (ADR 0116).
	deleted := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableDeleted),
		envelope("acme", EventDatabaseTableDeleted, "database", t0.Add(2*time.Minute), map[string]any{"schema": "nfl_data", "name": "players"}))
	if deleted != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("deleted = %+v", deleted)
	}
	if ds = listPostgresTables(t, svc, "acme"); len(ds) != 1 || !ds[0].Missing || ds[0].Name != "nfl_data.players" {
		t.Errorf("after delete: %+v", ds)
	}

	// A later created clears Missing.
	recreated := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableCreated),
		envelope("acme", EventDatabaseTableCreated, "database", t0.Add(3*time.Minute), dbTableData("nfl_data", "players", nil)))
	if recreated != (Result{Action: Ack, Applied: true}) {
		t.Fatalf("recreated = %+v", recreated)
	}
	if ds = listPostgresTables(t, svc, "acme"); len(ds) != 1 || ds[0].Missing {
		t.Errorf("after recreate: %+v", ds)
	}
}

// A deleted event for a table the catalog never saw still records a Missing placeholder
// rather than erroring, so a later out-of-order created/updated has something to merge into.
func TestDatabaseTableHandle_DeletedForAnUnseenTableRecordsAPlaceholder(t *testing.T) {
	svc := newDataService()
	res := NewDatabaseTableProcessor(svc).Handle(ctx, dbTableSubject("acme", EventDatabaseTableDeleted),
		envelope("acme", EventDatabaseTableDeleted, "database", t0, map[string]any{"schema": "nfl_data", "name": "never_seen"}))
	if !res.Applied || res.Action != Ack {
		t.Fatalf("result = %+v", res)
	}
	ds := listPostgresTables(t, svc, "acme")
	if len(ds) != 1 || !ds[0].Missing || ds[0].Name != "nfl_data.never_seen" {
		t.Errorf("placeholder = %+v", ds)
	}
}

// A hand-registered format: "postgres" row (ADR 0102) is merged into, not duplicated: its
// description, tags and owner survive; only columns/kind/approxRows and Missing change.
func TestDatabaseTableHandle_MergesIntoAHandRegisteredRow(t *testing.T) {
	svc := newDataService()
	actor := asset.Actor{Subject: "sub-alice", DisplayName: "alice@example.com"}
	hand, err := svc.Create(ctx, "acme", actor, data.Input{
		Name: "NFL Players", Description: "Hand-curated roster table", Tags: []string{"sports"}, Owner: "alice",
		Format: data.FormatPostgres, PostgresTable: &data.PostgresTableRef{Schema: "nfl_data", Name: "players"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create stamps the hand-registered row's UpdatedAt from the real clock; the merge event
	// must be newer than that to apply, so it's timed off time.Now() rather than the fixed t0
	// other tests use (which predates it).
	mergeAt := time.Now().UTC().Add(time.Hour)
	res := NewDatabaseTableProcessor(svc).Handle(ctx, dbTableSubject("acme", EventDatabaseTableCreated),
		envelope("acme", EventDatabaseTableCreated, "database", mergeAt, dbTableData("nfl_data", "players", map[string]any{
			"approxRows": int64(42), "comment": "auto-generated table comment",
		})))
	if !res.Applied || res.Action != Ack {
		t.Fatalf("result = %+v", res)
	}

	got, err := svc.Get(ctx, "acme", hand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "NFL Players" || got.Description != "Hand-curated roster table" || got.Owner != "alice" || len(got.Tags) != 1 || got.Tags[0] != "sports" {
		t.Errorf("merge changed hand-set fields: %+v", got)
	}
	if got.Source != data.SourceManual {
		t.Errorf("Source = %q, want it to stay manual after a merge", got.Source)
	}
	if got.Kind != data.TableKindTable || got.ApproxRows == nil || *got.ApproxRows != 42 || len(got.Schema) != 1 {
		t.Errorf("merge didn't refresh the introspected fields: %+v", got)
	}

	// Still editable through the manual write API — merging never took over its editability.
	if _, err := svc.Update(ctx, "acme", hand.ID, data.Input{
		Name: "NFL Players", Description: "edited", Format: data.FormatPostgres,
		PostgresTable: &data.PostgresTableRef{Schema: "nfl_data", Name: "players"},
	}); err != nil {
		t.Errorf("manual Update of a merged hand-registered row: %v", err)
	}
}

// An auto row (nothing hand-typed its description) tracks the real table's own comment —
// unlike a hand-registered row, which TestDatabaseTableHandle_MergesIntoAHandRegisteredRow
// confirms never gets overwritten.
func TestDatabaseTableHandle_AutoRowDescriptionTracksTheComment(t *testing.T) {
	svc := newDataService()
	p := NewDatabaseTableProcessor(svc)

	created := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableCreated),
		envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "players", map[string]any{"comment": "the NFL roster"})))
	if !created.Applied || created.Action != Ack {
		t.Fatalf("created = %+v", created)
	}
	ds := listPostgresTables(t, svc, "acme")
	if len(ds) != 1 || ds[0].Description != "the NFL roster" {
		t.Fatalf("Description = %q, want the comment", ds[0].Description)
	}

	// The comment changes; a later update refreshes it, since nothing hand-typed it.
	updated := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableUpdated),
		envelope("acme", EventDatabaseTableUpdated, "database", t0.Add(time.Minute), dbTableData("nfl_data", "players", map[string]any{"comment": "renamed comment"})))
	if !updated.Applied || updated.Action != Ack {
		t.Fatalf("updated = %+v", updated)
	}
	if ds = listPostgresTables(t, svc, "acme"); ds[0].Description != "renamed comment" {
		t.Errorf("Description = %q, want the refreshed comment", ds[0].Description)
	}

	// A null comment (no comment set on the real table) clears it the same way.
	cleared := p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableUpdated),
		envelope("acme", EventDatabaseTableUpdated, "database", t0.Add(2*time.Minute), dbTableData("nfl_data", "players", map[string]any{"comment": nil})))
	if !cleared.Applied || cleared.Action != Ack {
		t.Fatalf("cleared = %+v", cleared)
	}
	if ds = listPostgresTables(t, svc, "acme"); ds[0].Description != "" {
		t.Errorf("Description = %q, want cleared by a null comment", ds[0].Description)
	}
}

func TestDatabaseTableHandle_StaleAndRedeliveredEvents(t *testing.T) {
	svc := newDataService()
	p := NewDatabaseTableProcessor(svc)
	send := func(at time.Time, kind string) Result {
		return p.Handle(ctx, dbTableSubject("acme", EventDatabaseTableUpdated),
			envelope("acme", EventDatabaseTableUpdated, "database", at, dbTableData("nfl_data", "players", map[string]any{"kind": kind})))
	}

	send(t0.Add(10*time.Second), "view")

	stale := send(t0, "materialized_view")
	if stale.Action != Ack || stale.Applied || !strings.Contains(stale.Reason, "stale") {
		t.Errorf("stale = %+v; want acked (nothing to retry) and not applied", stale)
	}
	if ds := listPostgresTables(t, svc, "acme"); ds[0].Kind != data.TableKindView {
		t.Errorf("a stale event rolled the table back: %q", ds[0].Kind)
	}

	if again := send(t0.Add(10*time.Second), "view"); again.Action != Ack {
		t.Errorf("redelivery = %+v", again)
	}
}

func TestDatabaseTableHandle_WorkspacesAreScopedBySubject(t *testing.T) {
	svc := newDataService()
	p := NewDatabaseTableProcessor(svc)
	for _, ws := range []string{"acme", "globex"} {
		res := p.Handle(ctx, dbTableSubject(ws, EventDatabaseTableCreated), envelope(ws, EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "players", nil)))
		if !res.Applied {
			t.Fatalf("%s: %+v", ws, res)
		}
	}
	a, g := listPostgresTables(t, svc, "acme"), listPostgresTables(t, svc, "globex")
	if len(a) != 1 || len(g) != 1 {
		t.Errorf("acme=%+v globex=%+v; the same table in two workspaces must be two datasets", a, g)
	}
}

// A table's computed name colliding with an unrelated, already-registered dataset can never
// succeed by retrying — it needs a human to rename one of them.
func TestDatabaseTableHandle_DropsOnANameCollision(t *testing.T) {
	svc := newDataService()
	actor := asset.Actor{Subject: "sub-alice", DisplayName: "alice@example.com"}
	if _, err := svc.Create(ctx, "acme", actor, data.Input{Name: "nfl_data.players", Location: asset.Location{BackendID: "lake", Path: "x"}}); err != nil {
		t.Fatal(err)
	}
	res := NewDatabaseTableProcessor(svc).Handle(ctx, dbTableSubject("acme", EventDatabaseTableCreated),
		envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "players", nil)))
	if res.Action != Drop || res.Applied {
		t.Fatalf("name-colliding table = %+v, want Drop", res)
	}
}

// Unknown fields in the event payload (booth-database's shape is still provisional) are
// ignored, never a reason to drop an otherwise-valid event.
func TestDatabaseTableHandle_IgnoresUnknownFields(t *testing.T) {
	svc := newDataService()
	res := NewDatabaseTableProcessor(svc).Handle(ctx, dbTableSubject("acme", EventDatabaseTableCreated),
		envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "players", map[string]any{
			"schemaHash": "sha256:deadbeef", "somethingBoothDatabaseAddsLater": 42,
		})))
	if !res.Applied || res.Action != Ack {
		t.Fatalf("result = %+v", res)
	}
}

// Anything that can never succeed is dropped — retrying it would loop forever. Mirrors
// TestTableHandle_DropsWhatCanNeverBeApplied for the database.table.* event family.
func TestDatabaseTableHandle_DropsWhatCanNeverBeApplied(t *testing.T) {
	good := dbTableData("nfl_data", "players", nil)
	cases := []struct {
		name    string
		subject string
		payload []byte
		reason  string
	}{
		{"not a database table subject", "booth.acme.table.created", envelope("acme", EventTableCreated, "lakehouse", t0, good), "not a database table"},
		{"wrong number of subject tokens", "booth.acme.database.table.created.extra", envelope("acme", EventDatabaseTableCreated, "database", t0, good), "not a database table"},
		{"unknown database table verb", "booth.acme.database.table.archived", envelope("acme", "database.table.archived", "database", t0, good), "not a database table"},
		{"garbage payload", dbTableSubject("acme", EventDatabaseTableCreated), []byte("not json"), "malformed event envelope"},
		{"empty payload", dbTableSubject("acme", EventDatabaseTableCreated), nil, "malformed event envelope"},
		{"envelope names another workspace", dbTableSubject("acme", EventDatabaseTableCreated), envelope("globex", EventDatabaseTableCreated, "database", t0, good), "does not match subject workspace"},
		{"envelope names another event type", dbTableSubject("acme", EventDatabaseTableCreated), envelope("acme", EventDatabaseTableDeleted, "database", t0, good), "does not match subject event type"},
		{"missing schema", dbTableSubject("acme", EventDatabaseTableCreated), envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("", "players", nil)), "schema"},
		{"missing name", dbTableSubject("acme", EventDatabaseTableCreated), envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "", nil)), "name"},
		{"missing kind", dbTableSubject("acme", EventDatabaseTableCreated), envelope("acme", EventDatabaseTableCreated, "database", t0, dbTableData("nfl_data", "players", map[string]any{"kind": ""})), "kind"},
		{"missing publishedBy", dbTableSubject("acme", EventDatabaseTableCreated), envelope("acme", EventDatabaseTableCreated, "", t0, good), "publishedBy"},
		{"missing publishedAt", dbTableSubject("acme", EventDatabaseTableCreated), []byte(`{"workspace":"acme","eventType":"database.table.created","publishedBy":"database","data":{"schema":"nfl_data","name":"players","kind":"table"}}`), "publishedAt"},
		{"delete without a schema", dbTableSubject("acme", EventDatabaseTableDeleted), envelope("acme", EventDatabaseTableDeleted, "database", t0, map[string]any{"name": "players"}), "schema"},
		{"invalid workspace slug", "booth.Not_A_Slug.database.table.created", envelope("Not_A_Slug", EventDatabaseTableCreated, "database", t0, good), "invalid workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newDataService()
			res := NewDatabaseTableProcessor(svc).Handle(ctx, tc.subject, tc.payload)
			if res.Action != Drop {
				t.Fatalf("action = %v, want Drop (reason %q)", res.Action, res.Reason)
			}
			if !strings.Contains(res.Reason, tc.reason) {
				t.Errorf("reason = %q, want it to mention %q", res.Reason, tc.reason)
			}
		})
	}
}
