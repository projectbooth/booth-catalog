package search

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

var ctx = context.Background()

type stub struct {
	hits []asset.Hit
	err  error
	// got records what it was asked, for asserting how the service called it.
	gotWorkspace, gotQuery string
	gotLimit               int
}

func (s *stub) Search(_ context.Context, ws, q string, limit int) ([]asset.Hit, error) {
	s.gotWorkspace, s.gotQuery, s.gotLimit = ws, q, limit
	return s.hits, s.err
}

func hit(t asset.Type, id, name string, nameMatch bool) asset.Hit {
	return asset.Hit{Type: t, ID: id, Name: name, NameMatch: nameMatch}
}

func ids(r Result) []string {
	out := make([]string, len(r.Hits))
	for i, h := range r.Hits {
		out[i] = h.ID
	}
	return out
}

func newService() (*Service, *stub, *stub, *stub) {
	d := &stub{hits: []asset.Hit{hit(asset.TypeData, "d1", "orders", true), hit(asset.TypeData, "d2", "zebra", false)}}
	c := &stub{hits: []asset.Hit{hit(asset.TypeCode, "c1", "order_parser", true)}}
	b := &stub{hits: []asset.Hit{hit(asset.TypeDashboard, "b1", "Sales", false)}}
	return New(map[asset.Type]Searcher{asset.TypeData: d, asset.TypeCode: c, asset.TypeDashboard: b}), d, c, b
}

func TestSearch_MergesAllTypesNameMatchesFirst(t *testing.T) {
	svc, d, _, _ := newService()
	r, err := svc.Search(ctx, "acme", "  order  ", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// name matches (order_parser < orders, case-insensitively) then the rest by name.
	if want := []string{"c1", "d1", "b1", "d2"}; !reflect.DeepEqual(ids(r), want) {
		t.Errorf("hits = %v, want %v", ids(r), want)
	}
	if r.Query != "order" {
		t.Errorf("Query = %q, want the trimmed query", r.Query)
	}
	if d.gotWorkspace != "acme" || d.gotQuery != "order" || d.gotLimit != asset.DefaultLimit {
		t.Errorf("searcher was called with %q %q %d", d.gotWorkspace, d.gotQuery, d.gotLimit)
	}
}

func TestSearch_TypeFilter(t *testing.T) {
	svc, d, c, b := newService()
	r, err := svc.Search(ctx, "acme", "x", []asset.Type{asset.TypeCode}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(r), []string{"c1"}) {
		t.Errorf("hits = %v", ids(r))
	}
	if d.gotQuery != "" || b.gotQuery != "" || c.gotQuery == "" {
		t.Error("a type that wasn't asked for was searched anyway")
	}
}

func TestSearch_TruncatesToTheLimitAfterMerging(t *testing.T) {
	svc, _, _, _ := newService()
	r, _ := svc.Search(ctx, "acme", "x", nil, 2)
	if want := []string{"c1", "d1"}; !reflect.DeepEqual(ids(r), want) {
		t.Errorf("hits = %v, want the best two overall %v", ids(r), want)
	}
	// Each type is asked for the full limit, so nothing that belongs in the merged top-N is left behind.
	svc, d, _, _ := newService()
	svc.Search(ctx, "acme", "x", nil, 2) //nolint:errcheck
	if d.gotLimit != 2 {
		t.Errorf("per-type limit = %d, want 2", d.gotLimit)
	}
	svc, d, _, _ = newService()
	svc.Search(ctx, "acme", "x", nil, 9999) //nolint:errcheck
	if d.gotLimit != asset.MaxLimit {
		t.Errorf("an oversized limit was passed through as %d, want it clamped to %d", d.gotLimit, asset.MaxLimit)
	}
}

func TestSearch_NoHitsIsAnEmptyNonNilSlice(t *testing.T) {
	svc := New(map[asset.Type]Searcher{asset.TypeData: &stub{}, asset.TypeCode: &stub{}, asset.TypeDashboard: &stub{}})
	r, err := svc.Search(ctx, "acme", "nothing", nil, 10)
	if err != nil || r.Hits == nil || len(r.Hits) != 0 {
		t.Errorf("Search = %#v, %v", r.Hits, err)
	}
}

func TestSearch_RejectsBadQueries(t *testing.T) {
	svc, _, _, _ := newService()
	for name, q := range map[string]string{
		"empty":          "",
		"blank":          "   \t ",
		"too long":       strings.Repeat("a", MaxQueryLen+1),
		"too many words": strings.Repeat("w ", MaxTerms+1),
	} {
		_, err := svc.Search(ctx, "acme", q, nil, 10)
		var ve *asset.ValidationError
		if !errors.As(err, &ve) || ve.Field != "q" {
			t.Errorf("%s: err = %v, want a validation error on q", name, err)
		}
	}
	// The boundary itself is fine, counted in characters not bytes.
	if _, err := svc.Search(ctx, "acme", strings.Repeat("é", MaxQueryLen), nil, 10); err != nil {
		t.Errorf("a query of exactly %d characters was rejected: %v", MaxQueryLen, err)
	}
}

func TestSearch_OneFailingTypeFailsTheSearch(t *testing.T) {
	// A partial answer that silently omits a whole asset type would read as "no such dashboard".
	svc, _, _, b := newService()
	b.err = errors.New("dashboards store down")
	if _, err := svc.Search(ctx, "acme", "x", nil, 10); err == nil || !strings.Contains(err.Error(), "dashboard") {
		t.Errorf("err = %v, want it to name the failing type", err)
	}
	if _, err := New(map[asset.Type]Searcher{}).Search(ctx, "acme", "x", nil, 10); err == nil {
		t.Error("a missing searcher was silently skipped")
	}
}
