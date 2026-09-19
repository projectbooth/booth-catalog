// Package search is the one entry point that spans all three asset types (ADR 0044): basic
// text search over names and descriptions, no ranking model and no faceting.
//
// It owns no data and no index. Each asset package answers a search over its own records
// (internal/data, internal/code, internal/dashboards each implement Searcher), and this
// package fans a query out to them, merges the answers and orders them. Keeping the query
// per-package is what lets the three catalogs still be split into separate repos later
// (ADR 0001): search would then fan out over the network instead of in-process, and nothing
// else would change.
//
// The mechanism (ADR 0044 leaves it open) is case-insensitive substring matching — ILIKE in
// Postgres — rather than full-text search. Full-text search would stem and tokenize ("orders"
// finds "order"), but it would also stop matching the partial identifiers people actually
// type into a catalog ("cust_ord", "q3-2"), and it needs per-language configuration a
// multi-tenant platform has no basis to choose. Substring matching does what "basic" asks
// with no configuration and no surprises; a real ranking/FTS layer is a v1 concern.
package search

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

const (
	// MaxQueryLen bounds a query in characters.
	MaxQueryLen = 200
	// MaxTerms bounds how many whitespace-separated terms a query may have: each becomes a
	// clause per searched column, so it caps the work one request can ask of the database.
	MaxTerms = 10
)

// CheckQuery trims and validates a query string. It is shared by the search endpoint and by
// the per-type list endpoints' q filter, so every text filter in the API obeys one rule and
// none can be used to make the database do unbounded work. required says whether an empty
// query is an error (search) or simply means "no filter" (a list).
func CheckQuery(q string, required bool) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		if required {
			return "", asset.Invalid("q", "is required")
		}
		return "", nil
	}
	if n := len([]rune(q)); n > MaxQueryLen {
		return "", asset.Invalid("q", "must be at most %d characters (got %d)", MaxQueryLen, n)
	}
	if n := len(asset.Terms(q)); n > MaxTerms {
		return "", asset.Invalid("q", "must have at most %d words (got %d)", MaxTerms, n)
	}
	return q, nil
}

// Searcher is one asset type's search over its own records. Implemented by the data, code and
// dashboards services.
type Searcher interface {
	// Search returns up to limit hits in workspace matching every term of query, name matches
	// first.
	Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error)
}

// Service fans a search out across the asset types.
type Service struct {
	sources map[asset.Type]Searcher
}

// New returns a Service over the given per-type searchers.
func New(sources map[asset.Type]Searcher) *Service { return &Service{sources: sources} }

// Result is the outcome of one search.
type Result struct {
	// Query is the query as searched (trimmed).
	Query string      `json:"query"`
	Hits  []asset.Hit `json:"hits"`
}

// Search runs query against the requested asset types (all of them when types is empty) and
// returns at most limit hits: name matches before description/tag matches, then by name.
func (s *Service) Search(ctx context.Context, workspace, query string, types []asset.Type, limit int) (Result, error) {
	query, err := CheckQuery(query, true)
	if err != nil {
		return Result{}, err
	}
	if len(types) == 0 {
		types = asset.Types
	}
	limit = asset.ClampLimit(limit)

	// Ask each type for a full `limit`: the merged top `limit` can't include more than that
	// from any one type, so nothing that belongs in the answer is left behind.
	results := make([][]asset.Hit, len(types))
	errs := make([]error, len(types))
	var wg sync.WaitGroup
	for i, t := range types {
		src, ok := s.sources[t]
		if !ok {
			return Result{}, fmt.Errorf("search: no searcher registered for asset type %q", t)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = src.Search(ctx, workspace, query, limit)
		}()
	}
	wg.Wait()

	hits := []asset.Hit{}
	for i, t := range types {
		if errs[i] != nil {
			return Result{}, fmt.Errorf("searching %s: %w", t, errs[i])
		}
		hits = append(hits, results[i]...)
	}
	asset.SortHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return Result{Query: query, Hits: hits}, nil
}
