package dashboards

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/db/dbtest"
)

var ctx = context.Background()

var base = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// at is the n-th second after a fixed instant: event times in these tests.
func at(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }

func upsert(ws, module, extID, name string, when int, sources ...Source) Upsert {
	return Upsert{
		Workspace: ws, SourceModule: module, ExternalID: extID, Name: name,
		Description: "About " + name, Owner: "alice", Path: "/" + module + "/dashboard/" + extID,
		Sources: sources, At: at(when), NewID: asset.NewID(), ReceivedAt: at(1000 + when),
	}
}

func mustApply(t *testing.T, s Store, u Upsert) {
	t.Helper()
	applied, err := s.Apply(ctx, u)
	if err != nil || !applied {
		t.Fatalf("Apply(%s@%s) = %v, %v; want applied", u.Name, u.At.Format("15:04:05"), applied, err)
	}
}

// find returns the live dashboard's catalog ID, or "" if there isn't one.
func find(t *testing.T, s Store, ws, name string) string {
	t.Helper()
	page, err := s.List(ctx, ws, ListFilter{Query: name})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range page.Items {
		if d.Name == name {
			return d.ID
		}
	}
	return ""
}

func dsrc(id string) Source { return Source{Type: SourceDataset, DatasetID: id} }
func lsrc(backend, path string) Source {
	return Source{Type: SourceLocation, BackendID: backend, Path: path}
}
func esrc(name string) Source { return Source{Type: SourceExternal, Name: name, System: "postgres"} }

func names(p asset.Page[Dashboard]) []string {
	out := make([]string, len(p.Items))
	for i, d := range p.Items {
		out[i] = d.Name
	}
	return out
}

func nameList(ds []Dashboard) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}

// runStoreTests is the contract every Store must satisfy, run against both implementations.
func runStoreTests(t *testing.T, newStore func(t *testing.T) Store) {
	t.Run("ApplyCreatesAndGetReturnsEverything", func(t *testing.T) {
		s := newStore(t)
		u := upsert("acme", "superset", "42", "Revenue", 5, lsrc("lake", "warehouse/orders"), dsrc("ds-1"), esrc("public.orders"))
		u.LineageComplete = true
		mustApply(t, s, u)

		got, err := s.Get(ctx, "acme", u.NewID)
		if err != nil {
			t.Fatal(err)
		}
		want := Dashboard{
			ID: u.NewID, Workspace: "acme", SourceModule: "superset", ExternalID: "42", Name: "Revenue",
			Description: "About Revenue", Owner: "alice", Path: "/superset/dashboard/42", LineageComplete: true,
			Sources:   []Source{lsrc("lake", "warehouse/orders"), dsrc("ds-1"), esrc("public.orders")}, // publisher's order preserved
			CreatedAt: u.ReceivedAt, UpdatedAt: u.At,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Get:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("GetWithNoSourcesReturnsAnEmptyNonNilSlice", func(t *testing.T) {
		s := newStore(t)
		u := upsert("acme", "metabase", "1", "Bare", 1)
		mustApply(t, s, u)
		got, err := s.Get(ctx, "acme", u.NewID)
		if err != nil || got.Sources == nil || len(got.Sources) != 0 {
			t.Errorf("Sources = %#v, %v; want an empty non-nil slice", got.Sources, err)
		}
	})

	t.Run("ApplyAgainUpdatesInPlaceAndReplacesSourcesWholesale", func(t *testing.T) {
		s := newStore(t)
		first := upsert("acme", "superset", "42", "Revenue", 1, dsrc("a"), dsrc("b"))
		mustApply(t, s, first)

		second := upsert("acme", "superset", "42", "Revenue (v2)", 2, dsrc("c"))
		second.Owner = "bob"
		mustApply(t, s, second)

		// Same dashboard: the first event's ID stands, not the second's NewID.
		got, err := s.Get(ctx, "acme", first.NewID)
		if err != nil {
			t.Fatalf("the catalog ID changed on update: %v", err)
		}
		if got.Name != "Revenue (v2)" || got.Owner != "bob" || !got.UpdatedAt.Equal(at(2)) || !got.CreatedAt.Equal(first.ReceivedAt) {
			t.Errorf("after update: %+v", got)
		}
		if !reflect.DeepEqual(got.Sources, []Source{dsrc("c")}) {
			t.Errorf("sources = %v, want only [c] (replaced, not merged)", got.Sources)
		}
		if page, _ := s.List(ctx, "acme", ListFilter{}); page.Total != 1 {
			t.Errorf("update created a second dashboard: total %d", page.Total)
		}

		// An event with no sources clears them.
		mustApply(t, s, upsert("acme", "superset", "42", "Revenue (v2)", 3))
		if got, _ = s.Get(ctx, "acme", first.NewID); len(got.Sources) != 0 {
			t.Errorf("sources after an empty update = %v", got.Sources)
		}
	})

	// Delivery is at-least-once and unordered: an old event turning up late must never roll
	// a dashboard back.
	t.Run("StaleEventsAreIgnored", func(t *testing.T) {
		s := newStore(t)
		newest := upsert("acme", "superset", "42", "Newest", 10, dsrc("new"))
		mustApply(t, s, newest)

		stale := upsert("acme", "superset", "42", "Stale", 5, dsrc("old"))
		if applied, err := s.Apply(ctx, stale); err != nil || applied {
			t.Errorf("Apply(stale) = %v, %v; want ignored", applied, err)
		}
		got, _ := s.Get(ctx, "acme", newest.NewID)
		if got.Name != "Newest" || !reflect.DeepEqual(got.Sources, []Source{dsrc("new")}) || !got.UpdatedAt.Equal(at(10)) {
			t.Errorf("a stale event changed the dashboard: %+v", got)
		}

		// A redelivery of the very same event is harmless: applied again, same state.
		if applied, err := s.Apply(ctx, newest); err != nil || !applied {
			t.Errorf("Apply(redelivery) = %v, %v; want applied", applied, err)
		}
		got, _ = s.Get(ctx, "acme", newest.NewID)
		if got.Name != "Newest" || len(got.Sources) != 1 {
			t.Errorf("redelivery changed the dashboard: %+v", got)
		}
	})

	t.Run("IdentityIsWorkspaceModuleAndExternalID", func(t *testing.T) {
		s := newStore(t)
		a := upsert("acme", "superset", "7", "Superset 7", 1)
		b := upsert("acme", "metabase", "7", "Metabase 7", 1) // same tool-local ID, other publisher
		c := upsert("globex", "superset", "7", "Globex 7", 1) // same everything, other workspace
		for _, u := range []Upsert{a, b, c} {
			mustApply(t, s, u)
		}
		if page, _ := s.List(ctx, "acme", ListFilter{}); page.Total != 2 {
			t.Errorf("acme has %d dashboards, want 2", page.Total)
		}
		if _, err := s.Get(ctx, "globex", a.NewID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Get across workspaces: %v, want ErrNotFound", err)
		}
		if _, err := s.Get(ctx, "acme", c.NewID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Get across workspaces: %v, want ErrNotFound", err)
		}
	})

	t.Run("RemoveHidesTheDashboardEverywhere", func(t *testing.T) {
		s := newStore(t)
		u := upsert("acme", "superset", "42", "Revenue", 1, dsrc("ds-1"))
		mustApply(t, s, u)
		keep := upsert("acme", "superset", "43", "Other", 1, dsrc("ds-1"))
		mustApply(t, s, keep)

		applied, err := s.Remove(ctx, "acme", "superset", "42", at(2))
		if err != nil || !applied {
			t.Fatalf("Remove = %v, %v", applied, err)
		}
		if _, err := s.Get(ctx, "acme", u.NewID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Get after remove: %v, want ErrNotFound", err)
		}
		if page, _ := s.List(ctx, "acme", ListFilter{}); !reflect.DeepEqual(names(page), []string{"Other"}) || page.Total != 1 {
			t.Errorf("List after remove = %v (total %d)", names(page), page.Total)
		}
		if hits, _ := s.Search(ctx, "acme", "Revenue", 10); len(hits) != 0 {
			t.Errorf("Search finds a removed dashboard: %+v", hits)
		}
		reading, _ := s.ReadingDataset(ctx, "acme", "ds-1", asset.Location{BackendID: "lake"})
		if !reflect.DeepEqual(nameList(reading), []string{"Other"}) {
			t.Errorf("ReadingDataset = %v, want only the surviving dashboard", nameList(reading))
		}
	})

	t.Run("ATombstoneResistsStaleResurrection", func(t *testing.T) {
		s := newStore(t)
		u := upsert("acme", "superset", "42", "Revenue", 1)
		mustApply(t, s, u)
		if applied, err := s.Remove(ctx, "acme", "superset", "42", at(5)); err != nil || !applied {
			t.Fatal(applied, err)
		}

		// The "updated" that was published BEFORE the delete arrives after it.
		if applied, err := s.Apply(ctx, upsert("acme", "superset", "42", "Revenue", 3)); err != nil || applied {
			t.Errorf("Apply(older than the delete) = %v, %v; want ignored", applied, err)
		}
		if id := find(t, s, "acme", "Revenue"); id != "" {
			t.Error("a stale event resurrected a deleted dashboard")
		}

		// A genuinely newer created/updated (the tool recreated it) does bring it back —
		// under the same catalog ID, as a fresh life.
		again := upsert("acme", "superset", "42", "Revenue reborn", 9)
		mustApply(t, s, again)
		got, err := s.Get(ctx, "acme", u.NewID)
		if err != nil {
			t.Fatalf("revived dashboard has a new ID: %v", err)
		}
		if got.Name != "Revenue reborn" || !got.CreatedAt.Equal(again.ReceivedAt) {
			t.Errorf("revived = %+v (CreatedAt should restart)", got)
		}
	})

	t.Run("RemovingAnUnknownDashboardStillRecordsTheTombstone", func(t *testing.T) {
		s := newStore(t)
		// The catalog missed the create; the delete arrives first, then the old update.
		if applied, err := s.Remove(ctx, "acme", "superset", "99", at(5)); err != nil || !applied {
			t.Fatal(applied, err)
		}
		if applied, _ := s.Apply(ctx, upsert("acme", "superset", "99", "Ghost", 2)); applied {
			t.Error("an event older than an unseen delete was applied")
		}
		if id := find(t, s, "acme", "Ghost"); id != "" {
			t.Error("a dashboard the catalog only ever saw deleted became visible")
		}
	})

	t.Run("AStaleRemoveIsIgnored", func(t *testing.T) {
		s := newStore(t)
		u := upsert("acme", "superset", "42", "Revenue", 10)
		mustApply(t, s, u)
		if applied, err := s.Remove(ctx, "acme", "superset", "42", at(4)); err != nil || applied {
			t.Errorf("Remove(older than the dashboard's latest state) = %v, %v; want ignored", applied, err)
		}
		if _, err := s.Get(ctx, "acme", u.NewID); err != nil {
			t.Errorf("a stale delete removed the dashboard: %v", err)
		}
	})

	t.Run("ListSortsPaginatesAndFilters", func(t *testing.T) {
		s := newStore(t)
		mk := func(module, id, name, owner, desc string) {
			u := upsert("acme", module, id, name, 1, dsrc("x"))
			u.Owner, u.Description = owner, desc
			mustApply(t, s, u)
		}
		mk("superset", "1", "zebra sales", "Alice", "quarterly numbers")
		mk("metabase", "1", "Apple traffic", "bob", "web analytics")
		mk("streamlit", "1", "model explorer", "alice", "an app for sales models")
		mk("superset", "2", "margins", "alice", "profit")

		cases := []struct {
			name string
			f    ListFilter
			want []string
		}{
			{"all, case-insensitively by name", ListFilter{}, []string{"Apple traffic", "margins", "model explorer", "zebra sales"}},
			{"query in a name", ListFilter{Query: "traffic"}, []string{"Apple traffic"}},
			{"query in a description", ListFilter{Query: "profit"}, []string{"margins"}},
			{"query terms are ANDed", ListFilter{Query: "sales models"}, []string{"model explorer"}},
			{"owner, case-insensitive", ListFilter{Owner: "ALICE"}, []string{"margins", "model explorer", "zebra sales"}},
			{"source module", ListFilter{SourceModule: "superset"}, []string{"margins", "zebra sales"}},
			{"module and owner", ListFilter{SourceModule: "superset", Owner: "alice", Query: "sales"}, []string{"zebra sales"}},
			{"first page", ListFilter{Limit: 2}, []string{"Apple traffic", "margins"}},
			{"second page", ListFilter{Limit: 2, Offset: 2}, []string{"model explorer", "zebra sales"}},
			{"no match", ListFilter{Query: "zzz"}, []string{}},
		}
		for _, tc := range cases {
			p, err := s.List(ctx, "acme", tc.f)
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
				continue
			}
			if !reflect.DeepEqual(names(p), tc.want) {
				t.Errorf("%s: got %v, want %v", tc.name, names(p), tc.want)
			}
		}
		p, _ := s.List(ctx, "acme", ListFilter{Limit: 1})
		if p.Total != 4 || len(p.Items) != 1 {
			t.Errorf("Total = %d with %d items, want the full match count", p.Total, len(p.Items))
		}
		if len(p.Items[0].Sources) != 0 {
			t.Errorf("a listing carries sources: %v", p.Items[0].Sources)
		}
		if beyond, _ := s.List(ctx, "acme", ListFilter{Offset: 99}); beyond.Items == nil || len(beyond.Items) != 0 {
			t.Errorf("past the end = %#v", beyond.Items)
		}
	})

	t.Run("ReadingDataset", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s, upsert("acme", "superset", "1", "by_id", 1, dsrc("ds-1")))
		mustApply(t, s, upsert("acme", "superset", "2", "by_exact_location", 1, lsrc("lake", "warehouse/orders")))
		mustApply(t, s, upsert("acme", "superset", "3", "by_deeper_location", 1, lsrc("lake", "warehouse/orders/2026/p.parquet")))
		mustApply(t, s, upsert("acme", "superset", "4", "wider_than_the_dataset", 1, lsrc("lake", "warehouse")))
		mustApply(t, s, upsert("acme", "superset", "5", "sibling_prefix", 1, lsrc("lake", "warehouse/orders-archive")))
		mustApply(t, s, upsert("acme", "superset", "6", "other_backend", 1, lsrc("archive", "warehouse/orders")))
		mustApply(t, s, upsert("acme", "superset", "7", "external_only", 1, esrc("warehouse/orders")))
		mustApply(t, s, upsert("acme", "superset", "8", "other_dataset", 1, dsrc("ds-2")))
		mustApply(t, s, upsert("acme", "superset", "9", "matches_twice", 1, dsrc("ds-1"), lsrc("lake", "warehouse/orders")))
		mustApply(t, s, upsert("globex", "superset", "1", "foreign_workspace", 1, dsrc("ds-1")))

		got, err := s.ReadingDataset(ctx, "acme", "ds-1", asset.Location{BackendID: "lake", Path: "warehouse/orders"})
		if err != nil {
			t.Fatal(err)
		}
		// Sorted by name; a dashboard matching two ways is listed once; only the dataset's own
		// folder and things inside it count — a reader of the parent folder, a name-prefix
		// sibling, another backend and an unresolvable external source do not.
		want := []string{"by_deeper_location", "by_exact_location", "by_id", "matches_twice"}
		if !reflect.DeepEqual(nameList(got), want) {
			t.Errorf("ReadingDataset = %v, want %v", nameList(got), want)
		}
		if len(got[0].Sources) != 0 {
			t.Error("ReadingDataset returned sources")
		}

		// A dataset registered at the backend root is read by everything located in that backend.
		root, _ := s.ReadingDataset(ctx, "acme", "ds-root", asset.Location{BackendID: "lake", Path: ""})
		if want := []string{"by_deeper_location", "by_exact_location", "matches_twice", "sibling_prefix", "wider_than_the_dataset"}; !reflect.DeepEqual(nameList(root), want) {
			t.Errorf("root dataset: %v, want %v", nameList(root), want)
		}
		none, err := s.ReadingDataset(ctx, "acme", "ds-nobody", asset.Location{BackendID: "nowhere", Path: "x"})
		if err != nil || none == nil || len(none) != 0 {
			t.Errorf("no readers = %#v, %v; want an empty non-nil slice", none, err)
		}
	})

	t.Run("ReadingDatasetComparesPathsLiterally", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s, upsert("acme", "superset", "1", "percent_dir", 1, lsrc("lake", "100%/x")))
		mustApply(t, s, upsert("acme", "superset", "2", "digits_dir", 1, lsrc("lake", "1000/x")))
		got, _ := s.ReadingDataset(ctx, "acme", "ds", asset.Location{BackendID: "lake", Path: "100%"})
		if !reflect.DeepEqual(nameList(got), []string{"percent_dir"}) {
			t.Errorf("a %% in a stored path was treated as a wildcard: %v", nameList(got))
		}
	})

	t.Run("Search", func(t *testing.T) {
		s := newStore(t)
		mk := func(module, id, name, desc string) {
			u := upsert("acme", module, id, name, 1)
			u.Description = desc
			mustApply(t, s, u)
		}
		mk("superset", "1", "zed_sales", "unrelated")
		mk("metabase", "1", "helper", "shows sales by region")
		mk("streamlit", "1", "Alpha_Sales", "unrelated")
		mk("superset", "2", "other", "nothing")
		mustApply(t, s, upsert("globex", "superset", "1", "sales_elsewhere", 1))

		hits, err := s.Search(ctx, "acme", "SALES", 10)
		if err != nil {
			t.Fatal(err)
		}
		var got, sources []string
		for _, h := range hits {
			if h.Type != asset.TypeDashboard || h.ID == "" {
				t.Errorf("malformed hit %+v", h)
			}
			got, sources = append(got, h.Name), append(sources, h.Source)
		}
		if want := []string{"Alpha_Sales", "zed_sales", "helper"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Search = %v, want %v (name matches first)", got, want)
		}
		if want := []string{"streamlit", "superset", "metabase"}; !reflect.DeepEqual(sources, want) {
			t.Errorf("hit sources = %v, want %v (the publishing module)", sources, want)
		}
		if l, _ := s.Search(ctx, "acme", "sales", 1); len(l) != 1 {
			t.Errorf("limit 1 returned %d", len(l))
		}
		if h, _ := s.Search(ctx, "acme", "   ", 10); h == nil || len(h) != 0 {
			t.Errorf("blank query = %#v", h)
		}
	})

	// Several catalog replicas consume the same durable subscription concurrently, so events
	// for one dashboard can race. Whatever the interleaving, the newest event must win and the
	// stored sources must belong to it — never a mixture of two events.
	t.Run("ConcurrentAppliersConvergeOnTheNewestEvent", func(t *testing.T) {
		s := newStore(t)
		const n = 10
		var wg sync.WaitGroup
		id := asset.NewID()
		for i := 1; i <= n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				u := upsert("acme", "superset", "42", fmt.Sprintf("v%02d", i), i, dsrc(fmt.Sprintf("a%02d", i)), dsrc(fmt.Sprintf("b%02d", i)))
				u.NewID = id
				if _, err := s.Apply(ctx, u); err != nil {
					t.Errorf("Apply(v%02d): %v", i, err)
				}
			}(i)
		}
		wg.Wait()

		got, err := s.Get(ctx, "acme", id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "v10" || !got.UpdatedAt.Equal(at(n)) {
			t.Errorf("final = %q at %v, want the newest event (v10)", got.Name, got.UpdatedAt)
		}
		if !reflect.DeepEqual(got.Sources, []Source{dsrc("a10"), dsrc("b10")}) {
			t.Errorf("sources = %v, want exactly the newest event's", got.Sources)
		}
	})

	t.Run("Ping", func(t *testing.T) {
		if err := newStore(t).Ping(ctx); err != nil {
			t.Error(err)
		}
	})
}

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(t *testing.T) Store { return NewMemoryStore() })
}

func TestPostgresStore(t *testing.T) {
	runStoreTests(t, func(t *testing.T) Store {
		s, err := NewPostgresStore(ctx, dbtest.Pool(t))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestPostgresStore_MigrationIsIdempotent(t *testing.T) {
	pool := dbtest.Pool(t)
	for i := 0; i < 3; i++ {
		if _, err := NewPostgresStore(ctx, pool); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}
