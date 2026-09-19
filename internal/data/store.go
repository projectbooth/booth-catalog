package data

import (
	"context"

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
	// with that name.
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
}
