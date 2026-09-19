package dashboards

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// fakeDatasets is the dataset catalog as the dashboard service sees it.
type fakeDatasets struct {
	byID  map[string]DatasetRef
	byLoc map[asset.Location][]DatasetRef
	err   error
}

func (f fakeDatasets) ByID(_ context.Context, _, id string) (DatasetRef, bool, error) {
	r, ok := f.byID[id]
	return r, ok, f.err
}

func (f fakeDatasets) ByLocation(_ context.Context, _ string, loc asset.Location) ([]DatasetRef, error) {
	return f.byLoc[loc], f.err
}

func newSvc(ds DatasetResolver) *Service { return NewService(NewMemoryStore(), ds) }

func baseUpsert(sources ...Source) Upsert {
	return Upsert{Workspace: "acme", SourceModule: "superset", ExternalID: "42", Name: "Revenue", Owner: "alice", At: at(1), Sources: sources}
}

func TestService_GetResolvesLineageAgainstTheCatalogAsItIsNow(t *testing.T) {
	orders := DatasetRef{ID: "ds-orders", Name: "orders"}
	orders2026 := DatasetRef{ID: "ds-2026", Name: "orders_2026"}
	ds := fakeDatasets{
		byID:  map[string]DatasetRef{"ds-orders": orders},
		byLoc: map[asset.Location][]DatasetRef{{BackendID: "lake", Path: "warehouse/orders/2026"}: {orders2026, orders}},
	}
	svc := newSvc(ds)

	if applied, err := svc.Apply(ctx, baseUpsert(
		dsrc("ds-orders"),                     // resolves by ID
		dsrc("ds-deleted"),                    // named a dataset that isn't (or is no longer) registered
		lsrc("lake", "warehouse/orders/2026"), // resolves to every dataset containing the location
		lsrc("lake", "elsewhere"),             // no dataset registered there
		esrc("public.orders"),                 // never resolved, even though a dataset is literally named like it
	)); err != nil || !applied {
		t.Fatalf("Apply = %v, %v", applied, err)
	}

	page, _ := svc.List(ctx, "acme", ListFilter{})
	d, err := svc.Get(ctx, "acme", page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Lineage.Complete {
		t.Error("lineage claimed complete though the publisher didn't say so")
	}
	got := make([][]DatasetRef, len(d.Lineage.Sources))
	for i, s := range d.Lineage.Sources {
		if s.Datasets == nil {
			t.Errorf("source %d has nil Datasets; want [] so JSON carries an array", i)
		}
		got[i] = s.Datasets
	}
	want := [][]DatasetRef{{orders}, {}, {orders2026, orders}, {}, {}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved datasets = %v\nwant %v", got, want)
	}
	// Every source is reported even where it doesn't resolve, as the publisher named it.
	if len(d.Lineage.Sources) != 5 || d.Lineage.Sources[4].Type != SourceExternal || d.Lineage.Sources[4].Name != "public.orders" {
		t.Errorf("sources = %+v", d.Lineage.Sources)
	}

	// Registering the missing dataset later connects it, with no event reprocessed.
	ds.byID["ds-deleted"] = DatasetRef{ID: "ds-deleted", Name: "late_arrival"}
	d, _ = svc.Get(ctx, "acme", page.Items[0].ID)
	if len(d.Lineage.Sources[1].Datasets) != 1 || d.Lineage.Sources[1].Datasets[0].Name != "late_arrival" {
		t.Errorf("lineage did not pick up a dataset registered after the dashboard: %+v", d.Lineage.Sources[1])
	}
}

func TestService_GetSurfacesResolverFailures(t *testing.T) {
	svc := newSvc(fakeDatasets{err: errors.New("dataset store down")})
	if _, err := svc.Apply(ctx, baseUpsert(dsrc("x"))); err != nil {
		t.Fatal(err)
	}
	page, _ := svc.List(ctx, "acme", ListFilter{})
	if _, err := svc.Get(ctx, "acme", page.Items[0].ID); err == nil || errors.Is(err, asset.ErrNotFound) {
		t.Errorf("Get = %v; a resolver failure must be an error, not a silently empty lineage", err)
	}
	// A dashboard with no resolvable sources doesn't touch the resolver at all.
	if _, err := svc.Apply(ctx, Upsert{Workspace: "acme", SourceModule: "superset", ExternalID: "1", Name: "NoSources", At: at(1)}); err != nil {
		t.Fatal(err)
	}
}

func TestService_ApplyValidatesAndDropsBadSourcesWithoutLosingTheDashboard(t *testing.T) {
	svc := newSvc(fakeDatasets{})
	u := baseUpsert(
		dsrc("ok"),
		Source{Type: "warp-drive"},  // unknown type
		lsrc("Bad Backend", "x"),    // invalid backend ID
		lsrc("lake", "../escape"),   // traversal
		Source{Type: SourceDataset}, // dataset source without an ID
		dsrc("ok"),                  // duplicate of the first
	)
	u.LineageComplete = true // the publisher's claim can't survive us dropping something

	if applied, err := svc.Apply(ctx, u); err != nil || !applied {
		t.Fatalf("Apply = %v, %v", applied, err)
	}
	page, _ := svc.List(ctx, "acme", ListFilter{})
	d, _ := svc.Get(ctx, "acme", page.Items[0].ID)
	if len(d.Lineage.Sources) != 1 || d.Lineage.Sources[0].DatasetID != "ok" {
		t.Errorf("sources = %+v, want only the one valid source", d.Lineage.Sources)
	}
	if d.LineageComplete || d.Lineage.Complete {
		t.Error("lineage still claims completeness after sources were dropped")
	}
}

func TestService_ApplyRejectsWhatCannotBeIndexed(t *testing.T) {
	svc := newSvc(fakeDatasets{})
	for _, tc := range []struct {
		name  string
		mod   func(*Upsert)
		field string
	}{
		{"bad workspace", func(u *Upsert) { u.Workspace = "Not A Slug" }, "workspace"},
		{"bad module id", func(u *Upsert) { u.SourceModule = "Super Set" }, "publishedBy"},
		{"missing dashboard id", func(u *Upsert) { u.ExternalID = " " }, "dashboardId"},
		{"missing name", func(u *Upsert) { u.Name = "" }, "name"},
		{"name too long", func(u *Upsert) { u.Name = strings.Repeat("n", 201) }, "name"},
		{"description too long", func(u *Upsert) { u.Description = strings.Repeat("d", 4001) }, "description"},
		{"no publish time", func(u *Upsert) { u.At = time.Time{} }, "publishedAt"},
		{"path leaves the shell", func(u *Upsert) { u.Path = "https://evil.example/x" }, "path"},
	} {
		u := baseUpsert()
		tc.mod(&u)
		_, err := svc.Apply(ctx, u)
		var ve *asset.ValidationError
		if !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: err = %v, want a %q validation error", tc.name, err, tc.field)
		}
	}
	if page, _ := svc.List(ctx, "acme", ListFilter{}); page.Total != 0 {
		t.Errorf("a rejected event indexed %d dashboard(s)", page.Total)
	}
}

func TestPathsMustStayInsideTheShell(t *testing.T) {
	// The UI links to this value, and a dashboard module is a third party to the catalog.
	for _, p := range []string{"", "/", "/superset/dashboard/42", "/superset/dashboard/42?tab=2#chart-3", "/a%20b"} {
		if got, err := normalizePath(p); err != nil || got != p {
			t.Errorf("normalizePath(%q) = %q, %v; want accepted unchanged", p, got, err)
		}
	}
	for _, p := range []string{
		"https://evil.example", "//evil.example/x", "javascript:alert(1)", "superset/dashboard/1", `/a\b`,
		"/has space", "/ctrl\x01char", "/nul\x00", "\t/x\n/../", strings.Repeat("/a", 300),
	} {
		got, err := normalizePath(p)
		// TrimSpace is applied first, so "\t/x\n/../" fails on its interior newline.
		if err == nil {
			t.Errorf("normalizePath(%q) = %q; want rejected", p, got)
		}
	}
}

func TestService_RemoveValidatesAndTombstones(t *testing.T) {
	svc := newSvc(fakeDatasets{})
	if _, err := svc.Apply(ctx, baseUpsert()); err != nil {
		t.Fatal(err)
	}
	if applied, err := svc.Remove(ctx, "acme", "superset", "42", at(2)); err != nil || !applied {
		t.Fatalf("Remove = %v, %v", applied, err)
	}
	if page, _ := svc.List(ctx, "acme", ListFilter{}); page.Total != 0 {
		t.Errorf("dashboard still listed after Remove")
	}
	for name, call := range map[string]func() (bool, error){
		"bad workspace": func() (bool, error) { return svc.Remove(ctx, "BAD", "superset", "42", at(2)) },
		"bad module":    func() (bool, error) { return svc.Remove(ctx, "acme", "", "42", at(2)) },
		"no id":         func() (bool, error) { return svc.Remove(ctx, "acme", "superset", "", at(2)) },
		"no time":       func() (bool, error) { return svc.Remove(ctx, "acme", "superset", "42", time.Time{}) },
	} {
		if _, err := call(); err == nil {
			t.Errorf("Remove(%s) accepted", name)
		}
	}
}

func TestService_ReadingDatasetPassesThrough(t *testing.T) {
	svc := newSvc(fakeDatasets{})
	if _, err := svc.Apply(ctx, baseUpsert(lsrc("lake", "warehouse/orders/2026"))); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ReadingDataset(ctx, "acme", "ds-1", asset.Location{BackendID: "lake", Path: "warehouse/orders"})
	if err != nil || len(got) != 1 || got[0].Name != "Revenue" {
		t.Errorf("ReadingDataset = %+v, %v", got, err)
	}
}
