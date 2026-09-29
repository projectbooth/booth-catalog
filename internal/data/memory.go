package data

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// MemoryStore is the in-process Store: dev mode (BOOTH_CATALOG_DEV_MEMORY) and fast unit
// tests. Nothing survives a restart. It implements exactly the semantics the Postgres store
// does — store_test.go holds both to one contract.
type MemoryStore struct {
	mu   sync.RWMutex
	byWS map[string]map[string]Dataset // workspace -> id -> dataset
	// tombstoned tracks a format: "iceberg" row removed by a table.deleted event; the row
	// itself is kept (see ApplyTable/RemoveTable) so a stale, late event can't resurrect it.
	// A FormatFile row is never tombstoned: Delete removes it outright, as it always has.
	tombstoned map[string]map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byWS: map[string]map[string]Dataset{}, tombstoned: map[string]map[string]bool{}}
}

func clone(d Dataset) Dataset {
	d.Schema = append([]Column{}, d.Schema...)
	d.Tags = append([]string{}, d.Tags...)
	if d.Table != nil {
		t := *d.Table
		d.Table = &t
	}
	return d
}

// nameTaken reports whether name is in use by a live (non-tombstoned) dataset other than
// exceptID. A name freed by a table.deleted tombstone is available again.
func (m *MemoryStore) nameTaken(ws, name, exceptID string) bool {
	for id, d := range m.byWS[ws] {
		if id != exceptID && d.Name == name && !m.tombstoned[ws][id] {
			return true
		}
	}
	return false
}

func (m *MemoryStore) ensureWorkspace(ws string) {
	if m.byWS[ws] == nil {
		m.byWS[ws] = map[string]Dataset{}
		m.tombstoned[ws] = map[string]bool{}
	}
}

// findTable returns the live-or-tombstoned row for (sourceModule, tableUUID), or nil.
func (m *MemoryStore) findTable(ws, sourceModule, uuid string) *Dataset {
	for _, d := range m.byWS[ws] {
		if d.Format == FormatIceberg && d.SourceModule == sourceModule && d.Table != nil && d.Table.UUID == uuid {
			c := clone(d)
			return &c
		}
	}
	return nil
}

func (m *MemoryStore) Create(_ context.Context, d Dataset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(d.Workspace, d.Name, "") {
		return asset.ErrExists
	}
	m.ensureWorkspace(d.Workspace)
	m.byWS[d.Workspace][d.ID] = clone(d)
	return nil
}

func (m *MemoryStore) Get(_ context.Context, ws, id string) (Dataset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.byWS[ws][id]
	if !ok || m.tombstoned[ws][id] {
		return Dataset{}, asset.ErrNotFound
	}
	return clone(d), nil
}

func (m *MemoryStore) Update(_ context.Context, d Dataset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.byWS[d.Workspace][d.ID]
	if !ok || m.tombstoned[d.Workspace][d.ID] {
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
	if _, ok := m.byWS[ws][id]; !ok || m.tombstoned[ws][id] {
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
	for id, d := range m.byWS[ws] {
		if m.tombstoned[ws][id] || !asset.MatchesAll(terms, searchText(d)...) {
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
	for id, d := range m.byWS[ws] {
		if !m.tombstoned[ws][id] && d.Location.BackendID == loc.BackendID && asset.PathContains(d.Location.Path, loc.Path) {
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
	for id, d := range m.byWS[ws] {
		if m.tombstoned[ws][id] {
			continue
		}
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
	for id, d := range m.byWS[ws] {
		if m.tombstoned[ws][id] || len(terms) == 0 || !asset.MatchesAll(terms, searchText(d)...) {
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

func (m *MemoryStore) ApplyTable(_ context.Context, u TableUpsert) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.findTable(u.Workspace, u.SourceModule, u.UUID)
	if cur != nil && cur.UpdatedAt.After(u.At) {
		return false, nil // stale
	}
	name := u.datasetName()
	exceptID := ""
	if cur != nil {
		exceptID = cur.ID
	}
	if m.nameTaken(u.Workspace, name, exceptID) {
		return false, asset.ErrExists
	}

	m.ensureWorkspace(u.Workspace)
	d := Dataset{ID: u.NewID, Workspace: u.Workspace, CreatedAt: u.ReceivedAt}
	if cur != nil {
		d.ID, d.CreatedAt = cur.ID, cur.CreatedAt
	}
	d.Format, d.SourceModule = FormatIceberg, u.SourceModule
	d.Name, d.Location, d.Schema = name, u.Location, u.Schema
	d.Table = &TableRef{Namespace: u.Namespace, Name: u.Name, UUID: u.UUID, CurrentSnapshotID: u.CurrentSnapshotID}
	d.UpdatedAt = u.At
	if m.tombstoned[u.Workspace][d.ID] {
		d.CreatedAt = u.ReceivedAt // reviving a tombstone starts a new life, like internal/dashboards
		delete(m.tombstoned[u.Workspace], d.ID)
	}
	m.byWS[u.Workspace][d.ID] = clone(d)
	return true, nil
}

func (m *MemoryStore) RemoveTable(_ context.Context, ws, sourceModule, tableUUID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.findTable(ws, sourceModule, tableUUID)
	if cur != nil && cur.UpdatedAt.After(at) {
		return false, nil // stale
	}
	m.ensureWorkspace(ws)
	d := Dataset{ID: asset.NewID(), Workspace: ws, Format: FormatIceberg, SourceModule: sourceModule,
		Table: &TableRef{UUID: tableUUID}, CreatedAt: at}
	if cur != nil {
		d = *cur
	}
	d.UpdatedAt = at
	m.byWS[ws][d.ID] = clone(d)
	m.tombstoned[ws][d.ID] = true
	return true, nil
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
