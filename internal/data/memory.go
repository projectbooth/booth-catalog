package data

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// MemoryStore is the in-process Store: dev mode (BOOTH_CATALOG_DEV_MEMORY) and fast unit
// tests. Nothing survives a restart. It implements exactly the semantics the Postgres store
// does — store_test.go holds both to one contract.
type MemoryStore struct {
	mu   sync.RWMutex
	byWS map[string]map[string]Dataset // workspace -> id -> dataset
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byWS: map[string]map[string]Dataset{}}
}

func clone(d Dataset) Dataset {
	d.Schema = append([]Column{}, d.Schema...)
	d.Tags = append([]string{}, d.Tags...)
	return d
}

func (m *MemoryStore) nameTaken(ws, name, exceptID string) bool {
	for id, d := range m.byWS[ws] {
		if id != exceptID && d.Name == name {
			return true
		}
	}
	return false
}

func (m *MemoryStore) Create(_ context.Context, d Dataset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(d.Workspace, d.Name, "") {
		return asset.ErrExists
	}
	if m.byWS[d.Workspace] == nil {
		m.byWS[d.Workspace] = map[string]Dataset{}
	}
	m.byWS[d.Workspace][d.ID] = clone(d)
	return nil
}

func (m *MemoryStore) Get(_ context.Context, ws, id string) (Dataset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.byWS[ws][id]
	if !ok {
		return Dataset{}, asset.ErrNotFound
	}
	return clone(d), nil
}

func (m *MemoryStore) Update(_ context.Context, d Dataset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.byWS[d.Workspace][d.ID]
	if !ok {
		return asset.ErrNotFound
	}
	if m.nameTaken(d.Workspace, d.Name, d.ID) {
		return asset.ErrExists
	}
	// Only the mutable fields change; identity and provenance are the store's to keep.
	cur.Name, cur.Description, cur.Location = d.Name, d.Description, d.Location
	cur.Schema, cur.Tags, cur.Owner, cur.UpdatedAt = d.Schema, d.Tags, d.Owner, d.UpdatedAt
	m.byWS[d.Workspace][d.ID] = clone(cur)
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, ws, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byWS[ws][id]; !ok {
		return asset.ErrNotFound
	}
	delete(m.byWS[ws], id)
	return nil
}

func sortByName(ds []Dataset) {
	sort.Slice(ds, func(i, j int) bool {
		a, b := strings.ToLower(ds[i].Name), strings.ToLower(ds[j].Name)
		if a != b {
			return a < b
		}
		return ds[i].ID < ds[j].ID
	})
}

// searchText is the text a query is matched against: name, description and tags.
func searchText(d Dataset) []string {
	return []string{d.Name, d.Description, strings.Join(d.Tags, " ")}
}

func (m *MemoryStore) List(_ context.Context, ws string, f ListFilter) (asset.Page[Dataset], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := asset.Terms(f.Query)
	var matched []Dataset
	for _, d := range m.byWS[ws] {
		if !asset.MatchesAll(terms, searchText(d)...) {
			continue
		}
		if !hasAllTags(d.Tags, f.Tags) {
			continue
		}
		if f.Owner != "" && !strings.EqualFold(d.Owner, f.Owner) {
			continue
		}
		matched = append(matched, clone(d))
	}
	sortByName(matched)

	total := len(matched)
	limit := asset.ClampLimit(f.Limit)
	start := f.Offset
	if start < 0 || start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return asset.Page[Dataset]{Items: append([]Dataset{}, matched[start:end]...), Total: total}, nil
}

func hasAllTags(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (m *MemoryStore) ContainingLocation(_ context.Context, ws string, loc asset.Location) ([]Dataset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Dataset
	for _, d := range m.byWS[ws] {
		if d.Location.BackendID == loc.BackendID && asset.PathContains(d.Location.Path, loc.Path) {
			out = append(out, clone(d))
		}
	}
	sortByName(out)
	return out, nil
}

func (m *MemoryStore) Tags(_ context.Context, ws string) ([]TagCount, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := map[string]int{}
	for _, d := range m.byWS[ws] {
		for _, t := range d.Tags {
			counts[t]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, TagCount{Tag: t, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out, nil
}

func (m *MemoryStore) Search(_ context.Context, ws, query string, limit int) ([]asset.Hit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := asset.Terms(query)
	hits := []asset.Hit{}
	for _, d := range m.byWS[ws] {
		if len(terms) == 0 || !asset.MatchesAll(terms, searchText(d)...) {
			continue
		}
		hits = append(hits, asset.Hit{
			Type: asset.TypeData, ID: d.ID, Name: d.Name, Description: d.Description, Owner: d.Owner,
			NameMatch: asset.MatchesAll(terms, d.Name),
		})
	}
	asset.SortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
