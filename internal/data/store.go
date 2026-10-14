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
	// with that name. d.Format is FormatFile or FormatPostgres here (ADR 0102): this is the
	// manual write path, and FormatIceberg is only ever created by ApplyTable.
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

	// ApplyPostgresTable upserts a format: "postgres" dataset from a database.table.created
	// or database.table.updated event (ADR 0116), identified by (workspace, schema, name) —
	// never a UUID, since a real Postgres table has none. If a matching row already exists —
	// hand-registered (ADR 0102) or previously auto-created — it is merged: Columns, Kind and
	// ApproxRows refresh and Missing clears, while Name, Description, Tags and Owner (and,
	// for a hand-registered row, Source) are left exactly as they are. Otherwise a new row is
	// created with Source=SourceAuto and no Description/Tags/Owner. Reports whether the event
	// was applied (false: stale, older than one already applied to the same identity).
	// asset.ErrExists means a freshly created row's computed name collides with an unrelated
	// existing dataset — the caller (the events Processor) treats that as unfixable.
	ApplyPostgresTable(ctx context.Context, u PostgresTableUpsert) (applied bool, err error)
	// RemovePostgresTable marks a database.table.deleted event's row Missing=true — visible,
	// with its search entry and lineage intact (ADR 0116 fork C), unlike RemoveTable's hidden
	// tombstone above. A table the catalog never saw still gets a Missing=true placeholder row
	// recorded, so a later out-of-order created/updated for the same identity still has
	// something to merge into rather than silently creating a duplicate.
	RemovePostgresTable(ctx context.Context, workspace, schema, name string, at time.Time) (applied bool, err error)
}
