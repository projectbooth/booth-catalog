package dashboards

import (
	"context"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// Store persists indexed dashboards. Every method is workspace-scoped (ADR 0008).
//
// Its central job is applying events that arrive at-least-once and possibly out of order
// (ADR 0021: JetStream redelivers, and several catalog replicas may consume concurrently).
// The rule is last-writer-wins by the event's publishedAt: an event older than one already
// applied to the same dashboard is a no-op, so a redelivered or reordered event can never roll
// a dashboard back. A delete leaves a tombstone carrying its publishedAt, so a stale
// "updated" arriving after a "deleted" cannot resurrect the dashboard — but a genuinely newer
// created/updated (the tool recreating it) does.
type Store interface {
	// Apply upserts a dashboard from a created/updated event, replacing its sources
	// wholesale. It reports whether the event was applied (false: stale, ignored).
	Apply(ctx context.Context, u Upsert) (applied bool, err error)
	// Remove tombstones a dashboard from a deleted event. It reports whether the event was
	// applied. Removing a dashboard the catalog never saw is not an error: it records the
	// tombstone, so an older event arriving late is still ignored.
	Remove(ctx context.Context, workspace, sourceModule, externalID string, at time.Time) (applied bool, err error)

	// Get returns a live (non-tombstoned) dashboard with its Sources.
	Get(ctx context.Context, workspace, id string) (Dashboard, error)
	// List returns live dashboards ordered by name. Sources are not populated.
	List(ctx context.Context, workspace string, f ListFilter) (asset.Page[Dashboard], error)
	// ReadingDataset returns the live dashboards that read a dataset: those with a
	// dataset-type source naming datasetID, or a location-type source lying within
	// datasetLoc (the dataset's registered location contains it — asset.PathContains).
	// Ordered by name. Sources are not populated.
	ReadingDataset(ctx context.Context, workspace, datasetID string, datasetLoc asset.Location) ([]Dashboard, error)

	// Search returns up to limit live dashboards matching query in name or description.
	Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error)
	Ping(ctx context.Context) error
}
