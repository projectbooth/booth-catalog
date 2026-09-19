package code

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// MemoryStore is the in-process Store: dev mode (BOOTH_CATALOG_DEV_MEMORY) and fast unit
// tests. It implements exactly the semantics the Postgres store does — store_test.go holds
// both to one contract.
type MemoryStore struct {
	mu   sync.RWMutex
	byWS map[string]map[string]*record // workspace -> id -> record
}

// record is an entry and its versions in publication order (index i has Seq i+1).
type record struct {
	entry    Entry
	versions []Version
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byWS: map[string]map[string]*record{}}
}

// view returns the entry with its derived fields filled in.
func (r *record) view() Entry {
	e := r.entry
	e.VersionCount = len(r.versions)
	if n := len(r.versions); n > 0 {
		latest := r.versions[n-1].VersionSummary
		e.LatestVersion = &latest
	}
	return e
}

func (m *MemoryStore) nameTaken(ws, name, exceptID string) bool {
	for id, r := range m.byWS[ws] {
		if id != exceptID && r.entry.Name == name {
			return true
		}
	}
	return false
}

func sized(v Version) Version {
	v.SizeBytes = len(v.Source)
	return v
}

func (m *MemoryStore) Create(_ context.Context, e Entry, first Version) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(e.Workspace, e.Name, "") {
		return asset.ErrExists
	}
	if m.byWS[e.Workspace] == nil {
		m.byWS[e.Workspace] = map[string]*record{}
	}
	first.Seq = 1
	e.LatestVersion, e.VersionCount = nil, 0 // derived, never stored
	m.byWS[e.Workspace][e.ID] = &record{entry: e, versions: []Version{sized(first)}}
	return nil
}

func (m *MemoryStore) Get(_ context.Context, ws, id string) (Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.byWS[ws][id]
	if !ok {
		return Entry{}, asset.ErrNotFound
	}
	return r.view(), nil
}

func (m *MemoryStore) Update(_ context.Context, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byWS[e.Workspace][e.ID]
	if !ok {
		return asset.ErrNotFound
	}
	if m.nameTaken(e.Workspace, e.Name, e.ID) {
		return asset.ErrExists
	}
	r.entry.Name, r.entry.Description, r.entry.Owner, r.entry.Language = e.Name, e.Description, e.Owner, e.Language
	r.entry.UpdatedAt = e.UpdatedAt
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

func searchText(e Entry) []string { return []string{e.Name, e.Description} }

func (m *MemoryStore) List(_ context.Context, ws string, f ListFilter) (asset.Page[Entry], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := asset.Terms(f.Query)
	var matched []Entry
	for _, r := range m.byWS[ws] {
		e := r.view()
		if !asset.MatchesAll(terms, searchText(e)...) {
			continue
		}
		if f.Owner != "" && !strings.EqualFold(e.Owner, f.Owner) {
			continue
		}
		if f.Language != "" && e.Language != f.Language {
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool {
		a, b := strings.ToLower(matched[i].Name), strings.ToLower(matched[j].Name)
		if a != b {
			return a < b
		}
		return matched[i].ID < matched[j].ID
	})

	total := len(matched)
	start := f.Offset
	if start < 0 || start > total {
		start = total
	}
	end := start + asset.ClampLimit(f.Limit)
	if end > total {
		end = total
	}
	return asset.Page[Entry]{Items: append([]Entry{}, matched[start:end]...), Total: total}, nil
}

func (m *MemoryStore) AddVersion(_ context.Context, ws, entryID string, v Version) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byWS[ws][entryID]
	if !ok {
		return Version{}, asset.ErrNotFound
	}
	for _, have := range r.versions {
		if have.Version == v.Version {
			return Version{}, asset.ErrExists
		}
	}
	v.Seq = len(r.versions) + 1
	v = sized(v)
	r.versions = append(r.versions, v)
	r.entry.UpdatedAt = v.PublishedAt
	return v, nil
}

func (m *MemoryStore) Versions(_ context.Context, ws, entryID string) ([]VersionSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.byWS[ws][entryID]
	if !ok {
		return nil, asset.ErrNotFound
	}
	out := make([]VersionSummary, 0, len(r.versions))
	for i := len(r.versions) - 1; i >= 0; i-- {
		out = append(out, r.versions[i].VersionSummary)
	}
	return out, nil
}

func (m *MemoryStore) GetVersion(_ context.Context, ws, entryID, version string) (Version, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.byWS[ws][entryID]
	if !ok {
		return Version{}, asset.ErrNotFound
	}
	for _, v := range r.versions {
		if v.Version == version {
			return v, nil
		}
	}
	return Version{}, asset.ErrNotFound
}

func (m *MemoryStore) Search(_ context.Context, ws, query string, limit int) ([]asset.Hit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := asset.Terms(query)
	hits := []asset.Hit{}
	for _, r := range m.byWS[ws] {
		e := r.entry
		if len(terms) == 0 || !asset.MatchesAll(terms, searchText(e)...) {
			continue
		}
		hits = append(hits, asset.Hit{
			Type: asset.TypeCode, ID: e.ID, Name: e.Name, Description: e.Description, Owner: e.Owner,
			NameMatch: asset.MatchesAll(terms, e.Name),
		})
	}
	asset.SortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
