package code

import (
	"context"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// Store persists code entries and their versions. Every method is scoped to a workspace
// (ADR 0008); an ID from another workspace is simply ErrNotFound. As with the dataset
// store, an in-memory and a Postgres implementation share one contract suite.
type Store interface {
	// Create inserts an entry together with its first version in one atomic step, so an
	// entry never exists without a version. first.Seq is assigned by the store (1). Returns
	// asset.ErrExists if the workspace already has an entry with that name.
	Create(ctx context.Context, e Entry, first Version) error
	// Get returns the entry with LatestVersion and VersionCount populated.
	Get(ctx context.Context, workspace, id string) (Entry, error)
	// Update replaces an entry's editable metadata (name, description, owner, language) and
	// UpdatedAt. asset.ErrNotFound if absent; asset.ErrExists if the new name is taken.
	Update(ctx context.Context, e Entry) error
	// Delete removes an entry and all its versions.
	Delete(ctx context.Context, workspace, id string) error
	List(ctx context.Context, workspace string, f ListFilter) (asset.Page[Entry], error)

	// AddVersion publishes a new version of an existing entry, assigning the next Seq and
	// bumping the entry's UpdatedAt to v.PublishedAt. asset.ErrNotFound if the entry is
	// absent; asset.ErrExists if the label is already published — versions are immutable, so
	// republishing a label is refused rather than overwritten. Returns the stored version.
	AddVersion(ctx context.Context, workspace, entryID string, v Version) (Version, error)
	// Versions returns an entry's history without source, newest (highest Seq) first.
	// asset.ErrNotFound if the entry is absent.
	Versions(ctx context.Context, workspace, entryID string) ([]VersionSummary, error)
	// GetVersion returns one version with its source. asset.ErrNotFound if the entry or the
	// version is absent.
	GetVersion(ctx context.Context, workspace, entryID, version string) (Version, error)

	// Search returns up to limit entries matching query in name or description.
	Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error)
	Ping(ctx context.Context) error
}
