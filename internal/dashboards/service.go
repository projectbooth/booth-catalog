package dashboards

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// DatasetResolver lets this package read the dataset catalog without importing it: the
// data and dashboard catalogs stay separable along package boundaries (ADR 0001). The
// implementation is a small adapter over internal/data, wired in cmd/catalog.
type DatasetResolver interface {
	// ByID returns the dataset with that catalog ID, and false if there is none.
	ByID(ctx context.Context, workspace, id string) (DatasetRef, bool, error)
	// ByLocation returns the datasets whose registered location contains loc.
	ByLocation(ctx context.Context, workspace string, loc asset.Location) ([]DatasetRef, error)
}

// Service is the dashboard catalog's business logic: applying published events, and reading
// dashboards back with their lineage resolved against the dataset catalog.
type Service struct {
	store    Store
	datasets DatasetResolver
	now      func() time.Time
}

func NewService(store Store, datasets DatasetResolver) *Service {
	return &Service{store: store, datasets: datasets, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
}

// Apply indexes a dashboard from a created/updated event. It reports whether the event
// changed anything: false means it was stale (older than an event already applied), which is
// normal under at-least-once, out-of-order delivery and not an error.
func (s *Service) Apply(ctx context.Context, u Upsert) (bool, error) {
	u, dropped, err := u.Normalize()
	if err != nil {
		return false, err
	}
	if dropped > 0 {
		// The dashboard is still indexed; its lineage is marked incomplete. Say so in the
		// log so a publisher with a bad source shape is findable (ADR 0022: stdout logging).
		log.Printf("dashboards: %s/%s in workspace %s: dropped %d unusable lineage source(s); lineage marked incomplete", u.SourceModule, u.ExternalID, u.Workspace, dropped)
	}
	u.NewID, u.ReceivedAt = asset.NewID(), s.now()
	return s.store.Apply(ctx, u)
}

// Remove tombstones a dashboard from a deleted event. at is the event's publishedAt.
func (s *Service) Remove(ctx context.Context, workspace, sourceModule, externalID string, at time.Time) (bool, error) {
	if !asset.ValidWorkspace(workspace) {
		return false, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	if !moduleRE.MatchString(sourceModule) {
		return false, asset.Invalid("publishedBy", "must be a module ID such as \"superset\"")
	}
	externalID, err := asset.Text("dashboardId", externalID, maxExternalID, true, false)
	if err != nil {
		return false, err
	}
	if at.IsZero() {
		return false, asset.Invalid("publishedAt", "is required")
	}
	return s.store.Remove(ctx, workspace, sourceModule, externalID, at.UTC().Truncate(time.Microsecond))
}

// Get returns a dashboard with its lineage resolved against the dataset catalog as it is now.
func (s *Service) Get(ctx context.Context, workspace, id string) (Detail, error) {
	d, err := s.store.Get(ctx, workspace, id)
	if err != nil {
		return Detail{}, err
	}
	lineage, err := s.resolve(ctx, workspace, d)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Dashboard: d, Lineage: lineage}, nil
}

// resolve turns a dashboard's published sources into catalog datasets.
//
//   - a dataset source resolves to that dataset, if it's still registered;
//   - a location source resolves to every registered dataset whose location contains it;
//   - an external source never resolves: the catalog has no way to know what it names, and
//     guessing (matching a table name to a dataset name, say) would fabricate lineage — the
//     one thing ADR 0018 says a module must not do.
//
// An unresolved source is still returned, with no datasets, so the UI can show what the
// dashboard claims to read even where the catalog can't connect it.
func (s *Service) resolve(ctx context.Context, ws string, d Dashboard) (Lineage, error) {
	out := Lineage{Complete: d.LineageComplete, Sources: make([]ResolvedSource, 0, len(d.Sources))}
	for _, src := range d.Sources {
		rs := ResolvedSource{Source: src, Datasets: []DatasetRef{}}
		switch src.Type {
		case SourceDataset:
			ref, ok, err := s.datasets.ByID(ctx, ws, src.DatasetID)
			if err != nil {
				return Lineage{}, fmt.Errorf("resolving dataset %s: %w", src.DatasetID, err)
			}
			if ok {
				rs.Datasets = append(rs.Datasets, ref)
			}
		case SourceLocation:
			refs, err := s.datasets.ByLocation(ctx, ws, asset.Location{BackendID: src.BackendID, Path: src.Path})
			if err != nil {
				return Lineage{}, fmt.Errorf("resolving location %s:%s: %w", src.BackendID, src.Path, err)
			}
			rs.Datasets = append(rs.Datasets, refs...)
		}
		out.Sources = append(out.Sources, rs)
	}
	return out, nil
}

func (s *Service) List(ctx context.Context, workspace string, f ListFilter) (asset.Page[Dashboard], error) {
	return s.store.List(ctx, workspace, f)
}

// ReadingDataset returns the dashboards that read a dataset — the downstream half of lineage.
// The caller supplies the dataset's ID and registered location (it already holds the
// dataset), so this package needs no dataset lookup for it.
func (s *Service) ReadingDataset(ctx context.Context, workspace, datasetID string, loc asset.Location) ([]Dashboard, error) {
	return s.store.ReadingDataset(ctx, workspace, datasetID, loc)
}

func (s *Service) Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error) {
	return s.store.Search(ctx, workspace, query, limit)
}

func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }
