package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/db/dbtest"
)

var ctx = context.Background()

func sample(ws, name string) Dataset {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return Dataset{
		ID: asset.NewID(), Workspace: ws, Name: name,
		Description: "Description of " + name,
		Location:    asset.Location{BackendID: "lake", Path: "warehouse/" + name},
		Schema:      []Column{{Name: "id", Type: "bigint", Description: "key"}, {Name: "amount", Type: "decimal(10,2)"}},
		Tags:        []string{"finance", "pii"},
		Owner:       "alice", CreatedBy: "sub-alice", CreatedAt: now, UpdatedAt: now,
	}
}

func mustCreate(t *testing.T, s Store, d Dataset) Dataset {
	t.Helper()
	if err := s.Create(ctx, d); err != nil {
		t.Fatalf("Create(%s): %v", d.Name, err)
	}
	return d
}

func names(page asset.Page[Dataset]) []string {
	out := make([]string, len(page.Items))
	for i, d := range page.Items {
		out[i] = d.Name
	}
	return out
}

// runStoreTests is the contract every Store must satisfy. It runs against both the
// in-memory and the real PostgreSQL implementation, so they cannot drift apart.
func runStoreTests(t *testing.T, newStore func(t *testing.T) Store) {
	t.Run("CreateGetRoundTrip", func(t *testing.T) {
		s := newStore(t)
		want := mustCreate(t, s, sample("acme", "orders"))
		got, err := s.Get(ctx, "acme", want.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("EmptyCollectionsRoundTripAsEmptyNotNil", func(t *testing.T) {
		s := newStore(t)
		d := sample("acme", "bare")
		d.Schema, d.Tags = []Column{}, []string{}
		mustCreate(t, s, d)
		got, err := s.Get(ctx, "acme", d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Schema == nil || got.Tags == nil || len(got.Schema) != 0 || len(got.Tags) != 0 {
			t.Errorf("Schema=%#v Tags=%#v, want non-nil empty slices", got.Schema, got.Tags)
		}
	})

	t.Run("NameMustBeUniquePerWorkspace", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, sample("acme", "orders"))
		if err := s.Create(ctx, sample("acme", "orders")); !errors.Is(err, asset.ErrExists) {
			t.Errorf("duplicate name in one workspace: err = %v, want ErrExists", err)
		}
		// The same name in another workspace is a different dataset entirely.
		if err := s.Create(ctx, sample("globex", "orders")); err != nil {
			t.Errorf("same name in another workspace: %v", err)
		}
	})

	t.Run("WorkspacesAreHardBoundaries", func(t *testing.T) {
		s := newStore(t)
		d := mustCreate(t, s, sample("acme", "orders"))
		if _, err := s.Get(ctx, "globex", d.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Get from another workspace: err = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "globex", d.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Delete from another workspace: err = %v, want ErrNotFound", err)
		}
		other := d
		other.Workspace = "globex"
		if err := s.Update(ctx, other); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Update from another workspace: err = %v, want ErrNotFound", err)
		}
		page, err := s.List(ctx, "globex", ListFilter{})
		if err != nil || page.Total != 0 || len(page.Items) != 0 {
			t.Errorf("List in another workspace = %+v, %v; want empty", page, err)
		}
		// And the original is untouched by all of the above.
		if _, err := s.Get(ctx, "acme", d.ID); err != nil {
			t.Errorf("original disappeared: %v", err)
		}
	})

	t.Run("GetUnknownIsNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Get(ctx, "acme", "nope"); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("UpdateChangesMutableFieldsOnly", func(t *testing.T) {
		s := newStore(t)
		orig := mustCreate(t, s, sample("acme", "orders"))

		upd := orig
		upd.Name, upd.Description = "orders_v2", "rewritten"
		upd.Location = asset.Location{BackendID: "archive", Path: ""}
		upd.Schema = []Column{{Name: "only", Type: "text"}}
		upd.Tags = []string{"gold"}
		upd.Owner = "bob"
		upd.UpdatedAt = orig.UpdatedAt.Add(time.Hour)
		// A caller can't rewrite provenance through Update.
		upd.CreatedBy, upd.CreatedAt = "someone-else", orig.CreatedAt.Add(-time.Hour)
		if err := s.Update(ctx, upd); err != nil {
			t.Fatal(err)
		}

		got, err := s.Get(ctx, "acme", orig.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := upd
		want.CreatedBy, want.CreatedAt = orig.CreatedBy, orig.CreatedAt
		if !reflect.DeepEqual(got, want) {
			t.Errorf("after update:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("UpdateErrors", func(t *testing.T) {
		s := newStore(t)
		a := mustCreate(t, s, sample("acme", "a"))
		mustCreate(t, s, sample("acme", "b"))

		clash := a
		clash.Name = "b"
		if err := s.Update(ctx, clash); !errors.Is(err, asset.ErrExists) {
			t.Errorf("rename onto a taken name: err = %v, want ErrExists", err)
		}
		// Renaming to its own current name is not a clash.
		self := a
		self.Description = "edited"
		if err := s.Update(ctx, self); err != nil {
			t.Errorf("update keeping its own name: %v", err)
		}
		ghost := a
		ghost.ID = "ghost"
		if err := s.Update(ctx, ghost); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("update of unknown id: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		s := newStore(t)
		d := mustCreate(t, s, sample("acme", "orders"))
		if err := s.Delete(ctx, "acme", d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "acme", d.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("after delete: err = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "acme", d.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("second delete: err = %v, want ErrNotFound", err)
		}
		// The name is free again.
		if err := s.Create(ctx, sample("acme", "orders")); err != nil {
			t.Errorf("re-registering a deleted name: %v", err)
		}
	})

	t.Run("ListOrdersByNameCaseInsensitively", func(t *testing.T) {
		s := newStore(t)
		for _, n := range []string{"banana", "Apple", "cherry", "apple2"} {
			mustCreate(t, s, sample("acme", n))
		}
		page, err := s.List(ctx, "acme", ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"Apple", "apple2", "banana", "cherry"}; !reflect.DeepEqual(names(page), want) || page.Total != 4 {
			t.Errorf("names = %v total %d, want %v total 4", names(page), page.Total, want)
		}
	})

	t.Run("ListPagination", func(t *testing.T) {
		s := newStore(t)
		for i := 0; i < 7; i++ {
			mustCreate(t, s, sample("acme", fmt.Sprintf("ds%02d", i)))
		}
		p1, _ := s.List(ctx, "acme", ListFilter{Limit: 3})
		p2, _ := s.List(ctx, "acme", ListFilter{Limit: 3, Offset: 3})
		p3, _ := s.List(ctx, "acme", ListFilter{Limit: 3, Offset: 6})
		beyond, _ := s.List(ctx, "acme", ListFilter{Limit: 3, Offset: 50})
		if !reflect.DeepEqual(names(p1), []string{"ds00", "ds01", "ds02"}) ||
			!reflect.DeepEqual(names(p2), []string{"ds03", "ds04", "ds05"}) ||
			!reflect.DeepEqual(names(p3), []string{"ds06"}) {
			t.Errorf("pages = %v %v %v", names(p1), names(p2), names(p3))
		}
		for i, p := range []asset.Page[Dataset]{p1, p2, p3, beyond} {
			if p.Total != 7 {
				t.Errorf("page %d total = %d, want 7 (the full match count, not the page size)", i, p.Total)
			}
		}
		if len(beyond.Items) != 0 || beyond.Items == nil {
			t.Errorf("a page past the end = %#v, want an empty non-nil slice", beyond.Items)
		}
	})

	t.Run("ListFilters", func(t *testing.T) {
		s := newStore(t)
		a := sample("acme", "sales_orders")
		a.Description, a.Tags, a.Owner = "All customer orders", []string{"finance", "pii"}, "Alice"
		b := sample("acme", "web_events")
		b.Description, b.Tags, b.Owner = "Clickstream", []string{"pii", "raw"}, "bob"
		c := sample("acme", "hr_payroll")
		c.Description, c.Tags, c.Owner = "Payroll for orders of magnitude", []string{"finance"}, "alice"
		for _, d := range []Dataset{a, b, c} {
			mustCreate(t, s, d)
		}

		cases := []struct {
			name string
			f    ListFilter
			want []string
		}{
			{"no filter", ListFilter{}, []string{"hr_payroll", "sales_orders", "web_events"}},
			{"query matches a name", ListFilter{Query: "web"}, []string{"web_events"}},
			{"query matches a description", ListFilter{Query: "clickstream"}, []string{"web_events"}},
			{"query matches a tag", ListFilter{Query: "raw"}, []string{"web_events"}},
			{"query is case-insensitive", ListFilter{Query: "SALES"}, []string{"sales_orders"}},
			{"query is a substring match", ListFilter{Query: "rder"}, []string{"hr_payroll", "sales_orders"}},
			{"every query term must match, in any field", ListFilter{Query: "orders payroll"}, []string{"hr_payroll"}},
			{"no match", ListFilter{Query: "nonexistent"}, []string{}},
			{"one tag", ListFilter{Tags: []string{"pii"}}, []string{"sales_orders", "web_events"}},
			{"tags are ANDed", ListFilter{Tags: []string{"pii", "finance"}}, []string{"sales_orders"}},
			{"a tag nobody has", ListFilter{Tags: []string{"pii", "nope"}}, []string{}},
			{"owner is case-insensitive", ListFilter{Owner: "ALICE"}, []string{"hr_payroll", "sales_orders"}},
			{"owner is exact, not a substring", ListFilter{Owner: "ali"}, []string{}},
			{"filters combine with AND", ListFilter{Query: "orders", Tags: []string{"finance"}, Owner: "alice"}, []string{"hr_payroll", "sales_orders"}},
		}
		for _, tc := range cases {
			page, err := s.List(ctx, "acme", tc.f)
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
				continue
			}
			if !reflect.DeepEqual(names(page), tc.want) || page.Total != len(tc.want) {
				t.Errorf("%s: got %v (total %d), want %v", tc.name, names(page), page.Total, tc.want)
			}
		}
	})

	// A search for "100%" or "a_b" must find those literal characters, not run as a wildcard
	// (which would match everything). The two stores implement this differently, so it is
	// checked on both.
	t.Run("QueryWildcardsAreLiteral", func(t *testing.T) {
		s := newStore(t)
		pct := sample("acme", "growth")
		pct.Description = "up 100% year on year"
		und := sample("acme", "snake_case")
		plain := sample("acme", "snakeXcase")
		back := sample("acme", "windows")
		back.Description = `C:\data`
		for _, d := range []Dataset{pct, und, plain, back} {
			mustCreate(t, s, d)
		}
		for q, want := range map[string][]string{
			"100%":   {"growth"},
			"%":      {"growth"},
			"e_c":    {"snake_case"}, // "_" must not match "X"
			`c:\da`:  {"windows"},
			"snakex": {"snakeXcase"},
		} {
			page, err := s.List(ctx, "acme", ListFilter{Query: q})
			if err != nil {
				t.Errorf("%q: %v", q, err)
				continue
			}
			if !reflect.DeepEqual(names(page), want) {
				t.Errorf("List query %q = %v, want %v", q, names(page), want)
			}
			hits, err := s.Search(ctx, "acme", q, 10)
			if err != nil {
				t.Errorf("Search %q: %v", q, err)
				continue
			}
			var got []string
			for _, h := range hits {
				got = append(got, h.Name)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Search %q = %v, want %v", q, got, want)
			}
		}
	})

	t.Run("ContainingLocation", func(t *testing.T) {
		s := newStore(t)
		mk := func(name, backend, path string) {
			d := sample("acme", name)
			d.Location = asset.Location{BackendID: backend, Path: path}
			mustCreate(t, s, d)
		}
		mk("orders_folder", "lake", "warehouse/orders")
		mk("orders_2026", "lake", "warehouse/orders/2026")
		mk("orders_archive", "lake", "warehouse/orders-archive")
		mk("whole_lake", "lake", "")
		mk("other_backend", "archive", "warehouse/orders")
		mk("percent_path", "lake", "100%")
		other := sample("globex", "foreign")
		other.Location = asset.Location{BackendID: "lake", Path: "warehouse/orders"}
		mustCreate(t, s, other)

		find := func(backend, path string) []string {
			ds, err := s.ContainingLocation(ctx, "acme", asset.Location{BackendID: backend, Path: path})
			if err != nil {
				t.Fatalf("ContainingLocation(%s, %q): %v", backend, path, err)
			}
			out := []string{}
			for _, d := range ds {
				out = append(out, d.Name)
			}
			return out
		}
		cases := []struct {
			backend, path string
			want          []string
		}{
			{"lake", "warehouse/orders", []string{"orders_folder", "whole_lake"}},
			{"lake", "warehouse/orders/2026/part-1.parquet", []string{"orders_2026", "orders_folder", "whole_lake"}},
			// A dataset registered at a folder does not contain that folder's parent.
			{"lake", "warehouse", []string{"whole_lake"}},
			// A sibling that merely shares a name prefix is not an ancestor.
			{"lake", "warehouse/orders-archive/x", []string{"orders_archive", "whole_lake"}},
			{"archive", "warehouse/orders/x", []string{"other_backend"}},
			{"nowhere", "warehouse/orders", []string{}},
			// The stored path "100%" must be compared literally, never as a LIKE pattern.
			{"lake", "100%/x", []string{"percent_path", "whole_lake"}},
			{"lake", "1000/x", []string{"whole_lake"}},
		}
		for _, tc := range cases {
			if got := find(tc.backend, tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ContainingLocation(%s, %q) = %v, want %v", tc.backend, tc.path, got, tc.want)
			}
		}
	})

	t.Run("Tags", func(t *testing.T) {
		s := newStore(t)
		for name, tags := range map[string][]string{"a": {"pii", "finance"}, "b": {"pii"}, "c": {}} {
			d := sample("acme", name)
			d.Tags = tags
			mustCreate(t, s, d)
		}
		other := sample("globex", "z")
		other.Tags = []string{"secret"}
		mustCreate(t, s, other)

		got, err := s.Tags(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if want := []TagCount{{"finance", 1}, {"pii", 2}}; !reflect.DeepEqual(got, want) {
			t.Errorf("Tags = %v, want %v", got, want)
		}
		empty, err := s.Tags(ctx, "nobody")
		if err != nil || empty == nil || len(empty) != 0 {
			t.Errorf("Tags of an empty workspace = %#v, %v; want an empty non-nil slice", empty, err)
		}
	})

	t.Run("Search", func(t *testing.T) {
		s := newStore(t)
		mk := func(name, desc string, tags ...string) {
			d := sample("acme", name)
			d.Description, d.Tags = desc, tags
			mustCreate(t, s, d)
		}
		mk("zeta_orders", "unrelated")
		mk("alpha", "holds every order placed")
		mk("Beta_Orders", "unrelated")
		mk("gamma", "unrelated", "orders")
		mk("delta", "nothing to see")
		mustCreate(t, s, func() Dataset { d := sample("globex", "orders_elsewhere"); return d }())

		hits, err := s.Search(ctx, "acme", "ORDER", 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, h := range hits {
			if h.Type != asset.TypeData || h.ID == "" || h.Owner == "" {
				t.Errorf("malformed hit %+v", h)
			}
			got = append(got, h.Name)
		}
		// Name matches first (alphabetical, case-insensitive), then description/tag matches.
		if want := []string{"Beta_Orders", "zeta_orders", "alpha", "gamma"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Search order = %v, want %v", got, want)
		}

		limited, _ := s.Search(ctx, "acme", "order", 2)
		if len(limited) != 2 || limited[0].Name != "Beta_Orders" {
			t.Errorf("limit 2 = %+v", limited)
		}
		none, err := s.Search(ctx, "acme", "   ", 10)
		if err != nil || len(none) != 0 {
			t.Errorf("blank query = %v, %v; want no hits", none, err)
		}
		if h, _ := s.Search(ctx, "acme", "zzz", 10); h == nil || len(h) != 0 {
			t.Errorf("no match = %#v, want an empty non-nil slice", h)
		}
	})

	t.Run("Ping", func(t *testing.T) {
		if err := newStore(t).Ping(ctx); err != nil {
			t.Errorf("Ping: %v", err)
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

// A fresh migration run must be repeatable: every replica runs it at boot.
func TestPostgresStore_MigrationIsIdempotent(t *testing.T) {
	pool := dbtest.Pool(t)
	for i := 0; i < 3; i++ {
		if _, err := NewPostgresStore(ctx, pool); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}
