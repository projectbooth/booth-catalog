package data

import (
	"errors"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

var alice = asset.Actor{Subject: "sub-alice", DisplayName: "alice@example.com"}

func newSvc() *Service { return NewService(NewMemoryStore()) }

func TestService_CreateDefaultsOwnerToTheActor(t *testing.T) {
	svc := newSvc()
	in := validInput()

	d, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Owner != "alice@example.com" {
		t.Errorf("Owner = %q, want it defaulted to the registering user (ADR 0042)", d.Owner)
	}
	if d.CreatedBy != "sub-alice" {
		t.Errorf("CreatedBy = %q, want the token subject", d.CreatedBy)
	}
	if d.ID == "" || d.CreatedAt.IsZero() || !d.CreatedAt.Equal(d.UpdatedAt) {
		t.Errorf("ID/timestamps not assigned: %+v", d)
	}

	// An explicit owner (a person or a team) is kept, and is not the same thing as CreatedBy.
	in.Name, in.Owner = "other", "team-finance"
	d2, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Owner != "team-finance" || d2.CreatedBy != "sub-alice" {
		t.Errorf("Owner=%q CreatedBy=%q", d2.Owner, d2.CreatedBy)
	}
}

func TestService_CreateValidates(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Create(ctx, "acme", alice, Input{}); fieldOf(t, err) != "name" {
		t.Errorf("empty input: %v", err)
	}
	if _, err := svc.Create(ctx, "Not A Slug", alice, validInput()); fieldOf(t, err) != "workspace" {
		t.Errorf("bad workspace: %v", err)
	}
	if _, err := svc.Create(ctx, "acme", alice, validInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "acme", alice, validInput()); !errors.Is(err, asset.ErrExists) {
		t.Errorf("duplicate name: %v, want ErrExists", err)
	}
}

// An editor fixing a typo must not silently take ownership of someone else's dataset.
func TestService_UpdateKeepsTheOwnerUnlessGivenOne(t *testing.T) {
	svc := newSvc()
	in := validInput()
	in.Owner = "bob"
	d, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}

	in.Description, in.Owner = "fixed a typo", ""
	upd, err := svc.Update(ctx, "acme", d.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Owner != "bob" || upd.Description != "fixed a typo" {
		t.Errorf("after update: %+v", upd)
	}
	if upd.CreatedBy != d.CreatedBy || !upd.CreatedAt.Equal(d.CreatedAt) {
		t.Error("update rewrote provenance")
	}
	if !upd.UpdatedAt.After(d.UpdatedAt) && !upd.UpdatedAt.Equal(d.UpdatedAt) {
		t.Errorf("UpdatedAt went backwards: %v -> %v", d.UpdatedAt, upd.UpdatedAt)
	}

	in.Owner = "carol"
	if upd, err = svc.Update(ctx, "acme", d.ID, in); err != nil || upd.Owner != "carol" {
		t.Errorf("explicit reassignment: %+v, %v", upd, err)
	}

	got, _ := svc.Get(ctx, "acme", d.ID)
	if got.Owner != "carol" {
		t.Errorf("stored owner = %q", got.Owner)
	}
}

func TestService_UpdateErrors(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Update(ctx, "acme", "ghost", validInput()); !errors.Is(err, asset.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if _, err := svc.Update(ctx, "acme", "ghost", Input{}); fieldOf(t, err) != "name" {
		t.Errorf("invalid input is reported before existence: %v", err)
	}
}

// ADR 0102: a format: "postgres" dataset is registered and edited through the same manual
// write API as a "file" one — no event subscription, no ErrManagedExternally guard — and can
// freely switch between the two formats, since both are manually managed.
func TestService_PostgresFormatCreateAndUpdate(t *testing.T) {
	svc := newSvc()
	in := validPostgresInput()
	d, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Format != FormatPostgres || d.PostgresTable == nil || *d.PostgresTable != (PostgresTableRef{Schema: "public", Name: "orders"}) {
		t.Fatalf("created = %+v (table %+v)", d, d.PostgresTable)
	}
	if d.Location != (asset.Location{}) {
		t.Errorf("Location = %+v, want the zero value for a postgres dataset", d.Location)
	}

	got, err := svc.Get(ctx, "acme", d.ID)
	if err != nil || got.Format != FormatPostgres || got.PostgresTable == nil || got.PostgresTable.Name != "orders" {
		t.Errorf("Get after create = %+v, %v", got, err)
	}

	// A plain edit (e.g. renaming the real table) replaces the postgresTable wholesale.
	in.PostgresTable = &PostgresTableRef{Schema: "public", Name: "orders_v2"}
	upd, err := svc.Update(ctx, "acme", d.ID, in)
	if err != nil || upd.PostgresTable == nil || upd.PostgresTable.Name != "orders_v2" {
		t.Fatalf("after update: %+v, %v", upd, err)
	}

	// Switching to "file" clears the stale postgresTable and requires a location.
	fileIn := validInput()
	fileIn.Name = in.Name
	switched, err := svc.Update(ctx, "acme", d.ID, fileIn)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Format != FormatFile || switched.PostgresTable != nil || switched.Location == (asset.Location{}) {
		t.Errorf("switched to file = %+v (table %+v)", switched, switched.PostgresTable)
	}

	// And back again.
	switchedBack, err := svc.Update(ctx, "acme", d.ID, validPostgresInput())
	if err != nil {
		t.Fatal(err)
	}
	if switchedBack.Format != FormatPostgres || switchedBack.Location != (asset.Location{}) {
		t.Errorf("switched back to postgres = %+v", switchedBack)
	}
}

// The manual write API must refuse an attempt to create a format: "iceberg" row directly —
// that format is event-sourced only (TestService_ApplyTableIndexesAnIcebergDataset below).
func TestService_CreateRefusesIceberg(t *testing.T) {
	svc := newSvc()
	in := validInput()
	in.Format = FormatIceberg
	if _, err := svc.Create(ctx, "acme", alice, in); fieldOf(t, err) != "format" {
		t.Errorf("field = %v, want format", err)
	}
}

func TestService_ListNormalizesTagFilters(t *testing.T) {
	svc := newSvc()
	in := validInput()
	in.Tags = []string{"PII"}
	if _, err := svc.Create(ctx, "acme", alice, in); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, "acme", ListFilter{Tags: []string{" Pii "}})
	if err != nil || page.Total != 1 {
		t.Errorf("filter for \" Pii \" = %+v, %v; want the dataset tagged PII", page, err)
	}
	if _, err := svc.List(ctx, "acme", ListFilter{Tags: []string{"two words"}}); fieldOf(t, err) != "tags" {
		t.Errorf("malformed tag filter: %v", err)
	}
}

// TestService_ApplyTableIndexesAnIcebergDataset covers the whole ADR 0085 round trip: applying
// a table.* upsert creates a format: "iceberg" Dataset, updating it (by UUID, not name — a
// rename is just another update) changes it in place, and it is refused by the manual write API.
func TestService_ApplyTableIndexesAnIcebergDataset(t *testing.T) {
	svc := newSvc()
	snap := int64(42)
	u := TableUpsert{
		Workspace: "acme", SourceModule: "lakehouse", Namespace: "sales", Name: "orders", UUID: "tbl-1",
		Location:          asset.Location{BackendID: "lake", Path: "warehouse/sales/orders"},
		Schema:            []Column{{Name: "id", Type: "long"}},
		CurrentSnapshotID: &snap,
		At:                mustParseTime(t, "2026-09-28T12:00:00Z"),
	}
	applied, err := svc.ApplyTable(ctx, u)
	if err != nil || !applied {
		t.Fatalf("ApplyTable = %v, %v; want applied", applied, err)
	}

	page, err := svc.List(ctx, "acme", ListFilter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("List = %+v, %v", page, err)
	}
	d := page.Items[0]
	if d.Format != FormatIceberg || d.Name != "sales.orders" || d.Table == nil {
		t.Fatalf("indexed dataset = %+v", d)
	}
	if d.Table.Namespace != "sales" || d.Table.Name != "orders" || d.Table.UUID != "tbl-1" || d.Table.CurrentSnapshotID == nil || *d.Table.CurrentSnapshotID != 42 {
		t.Errorf("table ref = %+v", d.Table)
	}

	// A rename (namespace.name changes) is still the same row: identity is (module, UUID).
	u.Name = "orders_v2"
	u.At = mustParseTime(t, "2026-09-28T13:00:00Z")
	if applied, err = svc.ApplyTable(ctx, u); err != nil || !applied {
		t.Fatalf("rename ApplyTable = %v, %v", applied, err)
	}
	got, err := svc.Get(ctx, "acme", d.ID)
	if err != nil || got.Name != "sales.orders_v2" || got.ID != d.ID {
		t.Errorf("after rename: %+v, %v", got, err)
	}

	// A stale (older) event changes nothing.
	stale := u
	stale.Name, stale.At = "ignored", mustParseTime(t, "2026-09-28T12:30:00Z")
	if applied, err = svc.ApplyTable(ctx, stale); err != nil || applied {
		t.Errorf("stale ApplyTable = %v, %v; want not applied", applied, err)
	}

	// The manual write API refuses to touch it.
	if _, err := svc.Update(ctx, "acme", d.ID, validInput()); !errors.Is(err, asset.ErrManagedExternally) {
		t.Errorf("manual Update of an iceberg row: %v, want ErrManagedExternally", err)
	}
	if err := svc.Delete(ctx, "acme", d.ID); !errors.Is(err, asset.ErrManagedExternally) {
		t.Errorf("manual Delete of an iceberg row: %v, want ErrManagedExternally", err)
	}
}

// TestService_RemoveTableTombstonesAndBlocksResurrection mirrors internal/dashboards'
// identical staleness guarantee: a table.deleted removes the row from every read, and a
// late, older created/updated arriving afterward cannot bring it back.
func TestService_RemoveTableTombstonesAndBlocksResurrection(t *testing.T) {
	svc := newSvc()
	u := TableUpsert{
		Workspace: "acme", SourceModule: "lakehouse", Namespace: "sales", Name: "orders", UUID: "tbl-1",
		Location: asset.Location{BackendID: "lake", Path: "warehouse/sales/orders"},
		At:       mustParseTime(t, "2026-09-28T12:00:00Z"),
	}
	if _, err := svc.ApplyTable(ctx, u); err != nil {
		t.Fatal(err)
	}
	applied, err := svc.RemoveTable(ctx, "acme", "lakehouse", "tbl-1", mustParseTime(t, "2026-09-28T13:00:00Z"))
	if err != nil || !applied {
		t.Fatalf("RemoveTable = %v, %v; want applied", applied, err)
	}
	if page, _ := svc.List(ctx, "acme", ListFilter{}); page.Total != 0 {
		t.Errorf("tombstoned table still listed: %+v", page)
	}

	// A late created/updated from before the delete must not resurrect it.
	stale := u
	stale.Name, stale.At = "resurrected", mustParseTime(t, "2026-09-28T12:30:00Z")
	if applied, err = svc.ApplyTable(ctx, stale); err != nil || applied {
		t.Errorf("stale ApplyTable after delete = %v, %v; want not applied", applied, err)
	}
	if page, _ := svc.List(ctx, "acme", ListFilter{}); page.Total != 0 {
		t.Errorf("stale event resurrected a tombstoned table: %+v", page)
	}

	// A genuinely newer event (the table recreated) does bring it back.
	revived := u
	revived.At = mustParseTime(t, "2026-09-28T14:00:00Z")
	if applied, err = svc.ApplyTable(ctx, revived); err != nil || !applied {
		t.Fatalf("revive ApplyTable = %v, %v", applied, err)
	}
	if page, _ := svc.List(ctx, "acme", ListFilter{}); page.Total != 1 {
		t.Errorf("revived table not listed: %+v", page)
	}

	// Removing a table the catalog never saw records a tombstone rather than erroring.
	applied, err = svc.RemoveTable(ctx, "acme", "lakehouse", "never-seen", mustParseTime(t, "2026-09-28T15:00:00Z"))
	if err != nil || !applied {
		t.Errorf("RemoveTable of an unseen table = %v, %v; want applied (a recorded tombstone)", applied, err)
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestService_ContainingLocationNormalizes(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Create(ctx, "acme", alice, validInput()); err != nil { // lake:warehouse/orders
		t.Fatal(err)
	}
	ds, err := svc.ContainingLocation(ctx, "acme", asset.Location{BackendID: "lake", Path: "warehouse/orders/2026/"})
	if err != nil || len(ds) != 1 {
		t.Errorf("ContainingLocation = %v, %v", ds, err)
	}
	if _, err := svc.ContainingLocation(ctx, "acme", asset.Location{BackendID: "lake", Path: "../x"}); err == nil {
		t.Error("accepted a traversing path")
	}
}
