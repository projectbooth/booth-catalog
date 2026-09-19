package code

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

func sampleEntry(ws, name string) Entry {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return Entry{
		ID: asset.NewID(), Workspace: ws, Name: name, Description: "Description of " + name,
		Owner: "alice", Language: "python", CreatedBy: "sub-alice", CreatedAt: now, UpdatedAt: now,
	}
}

func sampleVersion(label string) Version {
	return Version{
		VersionSummary: VersionSummary{Version: label, Notes: "notes " + label, PublishedBy: "alice", PublishedAt: time.Now().UTC().Truncate(time.Microsecond)},
		Source:         "def f():\n    return '" + label + "'\n",
	}
}

func mustCreate(t *testing.T, s Store, ws, name string) Entry {
	t.Helper()
	e := sampleEntry(ws, name)
	if err := s.Create(ctx, e, sampleVersion("1.0.0")); err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return e
}

func mustAdd(t *testing.T, s Store, ws, id, label string) Version {
	t.Helper()
	v, err := s.AddVersion(ctx, ws, id, sampleVersion(label))
	if err != nil {
		t.Fatalf("AddVersion(%s): %v", label, err)
	}
	return v
}

func names(p asset.Page[Entry]) []string {
	out := make([]string, len(p.Items))
	for i, e := range p.Items {
		out[i] = e.Name
	}
	return out
}

func labels(vs []VersionSummary) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Version
	}
	return out
}

// runStoreTests is the contract every Store must satisfy, run against both implementations.
func runStoreTests(t *testing.T, newStore func(t *testing.T) Store) {
	t.Run("CreateGetRoundTrip", func(t *testing.T) {
		s := newStore(t)
		want := sampleEntry("acme", "clean_emails")
		first := sampleVersion("1.0.0")
		if err := s.Create(ctx, want, first); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "acme", want.ID)
		if err != nil {
			t.Fatal(err)
		}
		// Derived fields come from the versions; everything else is what was stored.
		if got.VersionCount != 1 || got.LatestVersion == nil {
			t.Fatalf("derived fields: count=%d latest=%v", got.VersionCount, got.LatestVersion)
		}
		wantLatest := first.VersionSummary
		wantLatest.Seq, wantLatest.SizeBytes = 1, len(first.Source)
		if !reflect.DeepEqual(*got.LatestVersion, wantLatest) {
			t.Errorf("latest = %+v, want %+v", *got.LatestVersion, wantLatest)
		}
		got.LatestVersion, got.VersionCount = nil, 0
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entry mismatch:\n got %+v\nwant %+v", got, want)
		}
		v, err := s.GetVersion(ctx, "acme", want.ID, "1.0.0")
		if err != nil || v.Source != first.Source || v.Seq != 1 {
			t.Errorf("GetVersion = %+v, %v", v, err)
		}
	})

	t.Run("NameMustBeUniquePerWorkspace", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "acme", "f")
		if err := s.Create(ctx, sampleEntry("acme", "f"), sampleVersion("1")); !errors.Is(err, asset.ErrExists) {
			t.Errorf("duplicate name: err = %v, want ErrExists", err)
		}
		if err := s.Create(ctx, sampleEntry("globex", "f"), sampleVersion("1")); err != nil {
			t.Errorf("same name in another workspace: %v", err)
		}
		// A refused create must not leave an orphaned version behind.
		if page, _ := s.List(ctx, "acme", ListFilter{}); page.Total != 1 {
			t.Errorf("acme has %d entries after a refused duplicate, want 1", page.Total)
		}
	})

	t.Run("WorkspacesAreHardBoundaries", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		if _, err := s.Get(ctx, "globex", e.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Get: %v", err)
		}
		if _, err := s.AddVersion(ctx, "globex", e.ID, sampleVersion("2")); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("AddVersion: %v", err)
		}
		if _, err := s.Versions(ctx, "globex", e.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Versions: %v", err)
		}
		if _, err := s.GetVersion(ctx, "globex", e.ID, "1.0.0"); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("GetVersion: %v", err)
		}
		if err := s.Delete(ctx, "globex", e.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Delete: %v", err)
		}
		if got, err := s.Get(ctx, "acme", e.ID); err != nil || got.VersionCount != 1 {
			t.Errorf("original disturbed: %+v, %v", got, err)
		}
	})

	t.Run("VersionsAreOrderedAndLatestIsMostRecentlyPublished", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f") // 1.0.0
		// Publication order, deliberately not label order: 0.9 after 1.0.0 is "latest".
		for _, l := range []string{"2.0.0", "0.9.0", "1.5.0"} {
			mustAdd(t, s, "acme", e.ID, l)
		}
		hist, err := s.Versions(ctx, "acme", e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"1.5.0", "0.9.0", "2.0.0", "1.0.0"}; !reflect.DeepEqual(labels(hist), want) {
			t.Errorf("history (newest first) = %v, want %v", labels(hist), want)
		}
		for i, h := range hist {
			if h.Seq != len(hist)-i {
				t.Errorf("history[%d].Seq = %d, want %d", i, h.Seq, len(hist)-i)
			}
			if h.SizeBytes == 0 {
				t.Errorf("history[%d] has no size", i)
			}
		}
		got, _ := s.Get(ctx, "acme", e.ID)
		if got.VersionCount != 4 || got.LatestVersion == nil || got.LatestVersion.Version != "1.5.0" || got.LatestVersion.Seq != 4 {
			t.Errorf("derived fields = count %d latest %+v", got.VersionCount, got.LatestVersion)
		}
	})

	// A consumer pinning a version needs it to mean the same thing forever.
	t.Run("PublishedVersionsAreImmutable", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		orig, _ := s.GetVersion(ctx, "acme", e.ID, "1.0.0")

		again := sampleVersion("1.0.0")
		again.Source = "print('a different source')"
		if _, err := s.AddVersion(ctx, "acme", e.ID, again); !errors.Is(err, asset.ErrExists) {
			t.Fatalf("republishing a label: err = %v, want ErrExists", err)
		}
		if now, _ := s.GetVersion(ctx, "acme", e.ID, "1.0.0"); !reflect.DeepEqual(now, orig) {
			t.Errorf("version changed after a refused republish:\n got %+v\nwant %+v", now, orig)
		}
		// Refusal must not burn a sequence number either.
		v2 := mustAdd(t, s, "acme", e.ID, "1.0.1")
		if v2.Seq != 2 {
			t.Errorf("next seq = %d, want 2 (a refused publish must not consume one)", v2.Seq)
		}
		// Labels are per entry: another entry can use "1.0.0".
		other := mustCreate(t, s, "acme", "g")
		if _, err := s.GetVersion(ctx, "acme", other.ID, "1.0.0"); err != nil {
			t.Errorf("another entry's 1.0.0: %v", err)
		}
	})

	t.Run("AddVersionBumpsUpdatedAtAndReturnsTheStoredVersion", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		v := sampleVersion("2.0.0")
		v.PublishedAt = e.UpdatedAt.Add(time.Hour)
		stored, err := s.AddVersion(ctx, "acme", e.ID, v)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Seq != 2 || stored.SizeBytes != len(v.Source) || stored.Source != v.Source {
			t.Errorf("stored = %+v", stored)
		}
		got, _ := s.Get(ctx, "acme", e.ID)
		if !got.UpdatedAt.Equal(v.PublishedAt) {
			t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, v.PublishedAt)
		}
	})

	t.Run("AddVersionToUnknownEntry", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.AddVersion(ctx, "acme", "ghost", sampleVersion("1")); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, err := s.Versions(ctx, "acme", "ghost"); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Versions: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetVersionUnknown", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		if _, err := s.GetVersion(ctx, "acme", e.ID, "9.9.9"); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	// Concurrent publishers must each get a distinct, gap-free sequence number: the
	// version history is only a history if its order is unambiguous.
	t.Run("ConcurrentPublishGetsDistinctSequenceNumbers", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		const n = 12
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := s.AddVersion(ctx, "acme", e.ID, sampleVersion(fmt.Sprintf("c%02d", i)))
				errs <- err
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("concurrent AddVersion: %v", err)
			}
		}
		hist, err := s.Versions(ctx, "acme", e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(hist) != n+1 {
			t.Fatalf("history has %d versions, want %d", len(hist), n+1)
		}
		for i, h := range hist { // newest first: n+1 down to 1
			if h.Seq != n+1-i {
				t.Errorf("seqs are not gap-free: hist[%d].Seq = %d, want %d", i, h.Seq, n+1-i)
			}
		}
	})

	t.Run("ConcurrentPublishOfOneLabelHasExactlyOneWinner", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		const n = 8
		var wg sync.WaitGroup
		results := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.AddVersion(ctx, "acme", e.ID, sampleVersion("race"))
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		wins, exists := 0, 0
		for err := range results {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, asset.ErrExists):
				exists++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		if wins != 1 || exists != n-1 {
			t.Errorf("wins=%d exists=%d, want 1 winner and %d refusals", wins, exists, n-1)
		}
	})

	t.Run("UpdateChangesMetadataNotVersions", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		mustAdd(t, s, "acme", e.ID, "1.1.0")

		upd := e
		upd.Name, upd.Description, upd.Owner, upd.Language = "f2", "rewritten", "bob", "sql"
		upd.UpdatedAt = e.UpdatedAt.Add(time.Hour)
		upd.CreatedBy, upd.CreatedAt = "someone-else", e.CreatedAt.Add(-time.Hour) // provenance is not editable
		if err := s.Update(ctx, upd); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(ctx, "acme", e.ID)
		if got.Name != "f2" || got.Description != "rewritten" || got.Owner != "bob" || got.Language != "sql" || !got.UpdatedAt.Equal(upd.UpdatedAt) {
			t.Errorf("after update: %+v", got)
		}
		if got.CreatedBy != e.CreatedBy || !got.CreatedAt.Equal(e.CreatedAt) {
			t.Error("update rewrote provenance")
		}
		if got.VersionCount != 2 || got.LatestVersion.Version != "1.1.0" {
			t.Errorf("update disturbed versions: count=%d latest=%+v", got.VersionCount, got.LatestVersion)
		}
	})

	t.Run("UpdateErrors", func(t *testing.T) {
		s := newStore(t)
		a := mustCreate(t, s, "acme", "a")
		mustCreate(t, s, "acme", "b")
		clash := a
		clash.Name = "b"
		if err := s.Update(ctx, clash); !errors.Is(err, asset.ErrExists) {
			t.Errorf("rename onto a taken name: %v", err)
		}
		ghost := a
		ghost.ID = "ghost"
		if err := s.Update(ctx, ghost); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("update of unknown id: %v", err)
		}
	})

	t.Run("DeleteRemovesTheEntryAndAllItsVersions", func(t *testing.T) {
		s := newStore(t)
		e := mustCreate(t, s, "acme", "f")
		mustAdd(t, s, "acme", e.ID, "1.1.0")
		if err := s.Delete(ctx, "acme", e.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "acme", e.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("Get after delete: %v", err)
		}
		if _, err := s.GetVersion(ctx, "acme", e.ID, "1.0.0"); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("GetVersion after delete: %v", err)
		}
		if err := s.Delete(ctx, "acme", e.ID); !errors.Is(err, asset.ErrNotFound) {
			t.Errorf("second delete: %v", err)
		}
		// Neither the name nor the version labels are haunted by the deleted entry.
		if err := s.Create(ctx, sampleEntry("acme", "f"), sampleVersion("1.0.0")); err != nil {
			t.Errorf("recreating after delete: %v", err)
		}
	})

	t.Run("ListSortsPaginatesAndFilters", func(t *testing.T) {
		s := newStore(t)
		mk := func(name, desc, owner, lang string) {
			e := sampleEntry("acme", name)
			e.Description, e.Owner, e.Language = desc, owner, lang
			if err := s.Create(ctx, e, sampleVersion("1")); err != nil {
				t.Fatal(err)
			}
		}
		mk("zip_files", "compress things", "Alice", "python")
		mk("Average", "mean of a column", "bob", "sql")
		mk("median", "middle value; sql window", "alice", "sql")
		mk("norm", "scale things", "alice", "python")

		cases := []struct {
			name string
			f    ListFilter
			want []string
		}{
			{"all, case-insensitively by name", ListFilter{}, []string{"Average", "median", "norm", "zip_files"}},
			{"query in a name", ListFilter{Query: "zip"}, []string{"zip_files"}},
			{"query in a description", ListFilter{Query: "column"}, []string{"Average"}},
			{"every term must match", ListFilter{Query: "things scale"}, []string{"norm"}},
			{"owner, case-insensitive", ListFilter{Owner: "ALICE"}, []string{"median", "norm", "zip_files"}},
			{"language", ListFilter{Language: "sql"}, []string{"Average", "median"}},
			{"combined", ListFilter{Owner: "alice", Language: "python", Query: "things"}, []string{"norm", "zip_files"}},
			{"a first page", ListFilter{Limit: 2}, []string{"Average", "median"}},
			{"a second page", ListFilter{Limit: 2, Offset: 2}, []string{"norm", "zip_files"}},
			{"nothing", ListFilter{Query: "zzz"}, []string{}},
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
		// Total is the match count, not the page size; every listed entry carries its latest version.
		p, _ := s.List(ctx, "acme", ListFilter{Limit: 1})
		if p.Total != 4 || len(p.Items) != 1 || p.Items[0].LatestVersion == nil || p.Items[0].VersionCount != 1 {
			t.Errorf("page = %+v", p)
		}
		beyond, _ := s.List(ctx, "acme", ListFilter{Offset: 99})
		if beyond.Items == nil || len(beyond.Items) != 0 || beyond.Total != 4 {
			t.Errorf("past the end = %#v total %d", beyond.Items, beyond.Total)
		}
	})

	t.Run("QueryWildcardsAreLiteral", func(t *testing.T) {
		s := newStore(t)
		a := sampleEntry("acme", "pct")
		a.Description = "handles 100% of cases"
		b := sampleEntry("acme", "snake_case")
		c := sampleEntry("acme", "snakeXcase")
		for _, e := range []Entry{a, b, c} {
			if err := s.Create(ctx, e, sampleVersion("1")); err != nil {
				t.Fatal(err)
			}
		}
		for q, want := range map[string][]string{"100%": {"pct"}, "e_c": {"snake_case"}} {
			p, _ := s.List(ctx, "acme", ListFilter{Query: q})
			if !reflect.DeepEqual(names(p), want) {
				t.Errorf("List %q = %v, want %v", q, names(p), want)
			}
			hits, _ := s.Search(ctx, "acme", q, 10)
			var got []string
			for _, h := range hits {
				got = append(got, h.Name)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Search %q = %v, want %v", q, got, want)
			}
		}
	})

	t.Run("Search", func(t *testing.T) {
		s := newStore(t)
		mk := func(ws, name, desc string) {
			e := sampleEntry(ws, name)
			e.Description = desc
			if err := s.Create(ctx, e, sampleVersion("1")); err != nil {
				t.Fatal(err)
			}
		}
		mk("acme", "zed_parse", "unrelated")
		mk("acme", "helper", "parses dates")
		mk("acme", "Alpha_Parser", "unrelated")
		mk("acme", "other", "nothing")
		mk("globex", "parse_elsewhere", "x")

		hits, err := s.Search(ctx, "acme", "PARS", 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, h := range hits {
			if h.Type != asset.TypeCode || h.ID == "" || h.Owner == "" {
				t.Errorf("malformed hit %+v", h)
			}
			got = append(got, h.Name)
		}
		if want := []string{"Alpha_Parser", "zed_parse", "helper"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Search = %v, want %v (name matches first)", got, want)
		}
		if l, _ := s.Search(ctx, "acme", "pars", 1); len(l) != 1 {
			t.Errorf("limit 1 returned %d", len(l))
		}
		if h, _ := s.Search(ctx, "acme", "  ", 10); len(h) != 0 || h == nil {
			t.Errorf("blank query = %#v", h)
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
