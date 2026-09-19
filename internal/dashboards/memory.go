package dashboards

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// MemoryStore is the in-process Store: dev mode (BOOTH_CATALOG_DEV_MEMORY) and fast unit
// tests. It implements exactly the semantics the Postgres store does — store_test.go holds
// both to one contract.
type MemoryStore struct {
	mu   sync.RWMutex
	byWS map[string]map[string]*Dashboard // workspace -> id -> dashboard (tombstones included)
	// tombstoned tracks which dashboards are deleted; a tombstone keeps its row so a stale
	// event can't resurrect it.
	tombstoned map[string]map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byWS: map[string]map[string]*Dashboard{}, tombstoned: map[string]map[string]bool{}}
}

func (m *MemoryStore) find(ws, module, externalID string) *Dashboard {
	for _, d := range m.byWS[ws] {
		if d.SourceModule == module && d.ExternalID == externalID {
			return d
		}
	}
	return nil
}

func (m *MemoryStore) Apply(_ context.Context, u Upsert) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.find(u.Workspace, u.SourceModule, u.ExternalID)
	if d != nil && d.UpdatedAt.After(u.At) {
		return false, nil // stale
	}
	if d == nil {
		if m.byWS[u.Workspace] == nil {
			m.byWS[u.Workspace] = map[string]*Dashboard{}
			m.tombstoned[u.Workspace] = map[string]bool{}
		}
		d = &Dashboard{ID: u.NewID, Workspace: u.Workspace, SourceModule: u.SourceModule, ExternalID: u.ExternalID, CreatedAt: u.ReceivedAt}
		m.byWS[u.Workspace][d.ID] = d
	}
	d.Name, d.Description, d.Owner, d.Path = u.Name, u.Description, u.Owner, u.Path
	d.LineageComplete = u.LineageComplete
	d.Sources = append([]Source{}, u.Sources...)
	d.UpdatedAt = u.At
	if m.tombstoned[u.Workspace][d.ID] {
		d.CreatedAt = u.ReceivedAt // reviving a tombstone starts a new life
		delete(m.tombstoned[u.Workspace], d.ID)
	}
	return true, nil
}

func (m *MemoryStore) Remove(_ context.Context, ws, module, externalID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.find(ws, module, externalID)
	if d != nil && d.UpdatedAt.After(at) {
		return false, nil
	}
	if d == nil {
		if m.byWS[ws] == nil {
			m.byWS[ws] = map[string]*Dashboard{}
			m.tombstoned[ws] = map[string]bool{}
		}
		d = &Dashboard{ID: asset.NewID(), Workspace: ws, SourceModule: module, ExternalID: externalID, CreatedAt: at}
		m.byWS[ws][d.ID] = d
	}
	d.UpdatedAt = at
	d.Sources = nil
	m.tombstoned[ws][d.ID] = true
	return true, nil
}

func (m *MemoryStore) live(ws, id string) (*Dashboard, bool) {
	d, ok := m.byWS[ws][id]
	if !ok || m.tombstoned[ws][id] {
		return nil, false
	}
	return d, true
}

func clone(d Dashboard) Dashboard {
	d.Sources = append([]Source{}, d.Sources...)
	return d
}

func (m *MemoryStore) Get(_ context.Context, ws, id string) (Dashboard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.live(ws, id)
	if !ok {
		return Dashboard{}, asset.ErrNotFound
	}
	return clone(*d), nil
}

func searchText(d *Dashboard) []string { return []string{d.Name, d.Description} }

func sortByName(ds []Dashboard) {
	sort.Slice(ds, func(i, j int) bool {
		a, b := strings.ToLower(ds[i].Name), strings.ToLower(ds[j].Name)
		if a != b {
			return a < b
		}
		return ds[i].ID < ds[j].ID
	})
}

func (m *MemoryStore) List(_ context.Context, ws string, f ListFilter) (asset.Page[Dashboard], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := asset.Terms(f.Query)
	var matched []Dashboard
	for id, d := range m.byWS[ws] {
		if m.tombstoned[ws][id] || !asset.MatchesAll(terms, searchText(d)...) {
			continue
		}
		if f.Owner != "" && !strings.EqualFold(d.Owner, f.Owner) {
			continue
		}
		if f.SourceModule != "" && d.SourceModule != f.SourceModule {
			continue
		}
		c := clone(*d)
		c.Sources = nil // the store contract: listings carry no sources
		matched = append(matched, c)
	}
	sortByName(matched)

	total := len(matched)
	start := f.Offset
	if start < 0 || start > total {
		start = total
	}
	end := start + asset.ClampLimit(f.Limit)
	if end > total {
		end = total
	}
	return asset.Page[Dashboard]{Items: append([]Dashboard{}, matched[start:end]...), Total: total}, nil
}

func (m *MemoryStore) ReadingDataset(_ context.Context, ws, datasetID string, loc asset.Location) ([]Dashboard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Dashboard{}
	for id, d := range m.byWS[ws] {
		if m.tombstoned[ws][id] {
			continue
		}
		for _, s := range d.Sources {
			byID := s.Type == SourceDataset && s.DatasetID == datasetID
			byLoc := s.Type == SourceLocation && s.BackendID == loc.BackendID && asset.PathContains(loc.Path, s.Path)
			if byID || byLoc {
				c := clone(*d)
				c.Sources = nil
				out = append(out, c)
				break
			}
		}
	}
	sortByName(out)
	return out, nil
}

func (m *MemoryStore) Search(_ context.Context, ws, query string, limit int) ([]asset.Hit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := asset.Terms(query)
	hits := []asset.Hit{}
	for id, d := range m.byWS[ws] {
		if m.tombstoned[ws][id] || len(terms) == 0 || !asset.MatchesAll(terms, searchText(d)...) {
			continue
		}
		hits = append(hits, asset.Hit{
			Type: asset.TypeDashboard, ID: d.ID, Name: d.Name, Description: d.Description, Owner: d.Owner,
			Source: d.SourceModule, NameMatch: asset.MatchesAll(terms, d.Name),
		})
	}
	asset.SortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
