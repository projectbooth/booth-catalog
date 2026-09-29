package data

import (
	"context"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// Store persists datasets. Every method is scoped to a workspace (ADR 0008): a workspace
// is a hard tenancy boundary, so an ID from another workspace is simply ErrNotFound.
//
// Two implementations exist — Postgres (production, ADR 0014) and in-memory (dev mode and
// fast tests) — and store_test.go runs one shared contract suite against both, so they
// can't silently drift apart.
type Store interface {
	// Create inserts d. It returns asset.ErrExists if the workspace already has a dataset
	// with that name. d.Format is always FormatFile here: this is the manual write path.
	Create(ctx context.Context, d Dataset) error
	Get(ctx context.Context, workspace, id string) (Dataset, error)
	// Update replaces d's mutable fields (everything but ID, Workspace, CreatedBy and
	// CreatedAt). asset.ErrNotFound if absent; asset.ErrExists if the new name is taken.
	Update(ctx context.Context, d Dataset) error
	Delete(ctx context.Context, workspace, id string) error
	List(ctx context.Context, workspace string, f ListFilter) (asset.Page[Dataset], error)
	// ContainingLocation returns the datasets whose registered location contains the given
	// one: same backend, and the dataset's path equal to, or a "/"-boundary ancestor of,
	// path. It is how dashboard lineage sources that name a storage location resolve to
	// catalog datasets. Ordered by name.
	ContainingLocation(ctx context.Context, workspace string, loc asset.Location) ([]Dataset, error)
	// Tags returns every tag in use and its dataset count, ordered by tag.
	Tags(ctx context.Context, workspace string) ([]TagCount, error)
	// Search returns up to limit datasets matching query in name, description or tags.
	Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error)
	// Ping reports whether the store is reachable, for the health check.
	Ping(ctx context.Context) error

	// ApplyTable upserts a format: "iceberg" dataset from a table.created/table.updated event
	// (ADR 0085), identified by (workspace, SourceModule, UUID) — never by name, since a
	// rename is just another update. It reports whether the event was applied (false: stale,
	// older than one already applied to the same table — the identical last-writer-wins rule
	// internal/dashboards.Store.Apply uses). asset.ErrExists means the table's computed name
	// collides with an unrelated existing dataset; the caller (the events Processor) treats
	// that as unfixable by retrying.
	ApplyTable(ctx context.Context, u TableUpsert) (applied bool, err error)
	// RemoveTable tombstones a table.deleted event's row, exactly as
	// internal/dashboards.Store.Remove tombstones a dashboard: the row is kept (invisible to
	// Get/List/Search/ContainingLocation) so a stale, late created/updated event can't
	// resurrect it. Removing a table the catalog never saw still records the tombstone.
	RemoveTable(ctx context.Context, workspace, sourceModule, tableUUID string, at time.Time) (applied bool, err error)
}
