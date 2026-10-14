package data

import (
	"context"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// Service is the dataset catalog's business logic: validation, ID and timestamp assignment,
// and owner defaulting, over a Store. Handlers call this, never the Store directly, so the
// rules hold no matter which HTTP route (or future caller) a write arrives through.
type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
}

// Create registers a dataset. An empty owner defaults to the actor (ADR 0042: "requires or
// defaults an owner") — the person registering it is the best available guess.
func (s *Service) Create(ctx context.Context, workspace string, actor asset.Actor, in Input) (Dataset, error) {
	if !asset.ValidWorkspace(workspace) {
		return Dataset{}, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	in, err := in.Normalize()
	if err != nil {
		return Dataset{}, err
	}
	if in.Owner == "" {
		in.Owner = actor.DisplayName
	}
	now := s.now()
	d := Dataset{
		ID: asset.NewID(), Workspace: workspace,
		Name: in.Name, Description: in.Description, Location: in.Location,
		Schema: in.Schema, Tags: in.Tags, Owner: in.Owner,
		CreatedBy: actor.Subject, CreatedAt: now, UpdatedAt: now,
		// in.Normalize already resolved this to FormatFile or FormatPostgres (ADR 0102) and
		// refused anything else: an Iceberg table is only ever created by ApplyTable, from a
		// table.* event, never through this write path.
		Format: in.Format, PostgresTable: in.PostgresTable,
	}
	if d.Format == FormatPostgres {
		// Every format: "postgres" row this write API creates is hand-registered by
		// definition (ADR 0116: an auto row only ever comes from ApplyPostgresTable).
		d.Source = SourceManual
	}
	if err := s.store.Create(ctx, d); err != nil {
		return Dataset{}, err
	}
	return d, nil
}

// Update replaces a dataset's mutable fields. An empty owner keeps the current one rather
// than resetting it to the caller: an editor tidying a description must not silently take
// ownership of someone else's dataset.
//
// A format: "iceberg" row, or a format: "postgres" row with Source == SourceAuto, refuses this
// (asset.ErrManagedExternally, ADR 0085/0116): it exists only because another module published
// an event about it, and a manual edit here would just be overwritten by that module's next
// event anyway — the same reason dashboards have no write API at all.
func (s *Service) Update(ctx context.Context, workspace, id string, in Input) (Dataset, error) {
	in, err := in.Normalize()
	if err != nil {
		return Dataset{}, err
	}
	cur, err := s.store.Get(ctx, workspace, id)
	if err != nil {
		return Dataset{}, err
	}
	if cur.Format == FormatIceberg || cur.Source == SourceAuto {
		return Dataset{}, asset.ErrManagedExternally
	}
	if in.Owner == "" {
		in.Owner = cur.Owner
	}
	cur.Name, cur.Description, cur.Location = in.Name, in.Description, in.Location
	cur.Schema, cur.Tags, cur.Owner = in.Schema, in.Tags, in.Owner
	// Format is as mutable as anything else above (ADR 0102 lets a manually-managed dataset
	// switch between "file" and "postgres"); PostgresTable moves with it, so switching away
	// from "postgres" clears a stale table reference and switching to it requires a fresh one
	// (in.Normalize already enforced that pairing). Source follows Format: a row this write API
	// touches is hand-registered by definition, whichever format it ends up as.
	cur.Format, cur.PostgresTable = in.Format, in.PostgresTable
	if cur.Format == FormatPostgres {
		cur.Source = SourceManual
	} else {
		cur.Source = ""
	}
	cur.UpdatedAt = s.now()
	if err := s.store.Update(ctx, cur); err != nil {
		return Dataset{}, err
	}
	return cur, nil
}

func (s *Service) Get(ctx context.Context, workspace, id string) (Dataset, error) {
	return s.store.Get(ctx, workspace, id)
}

// Delete removes a dataset. A format: "iceberg" row refuses this the same way Update does — it
// disappears from the catalog only via its own table.deleted event.
//
// A format: "postgres" auto row (ADR 0116) is almost the same: refused outright while the real
// table still exists (Missing is false), since the next database.table.updated would just
// recreate what was deleted. The one exception the ADR carves out is a Missing auto row — the
// real table is already gone, so there is nothing left for an event to resurrect against —
// which isOwner (the caller's workspace role is owner, not just editor) may remove by hand.
func (s *Service) Delete(ctx context.Context, workspace, id string, isOwner bool) error {
	cur, err := s.store.Get(ctx, workspace, id)
	if err != nil {
		return err
	}
	if cur.Format == FormatIceberg {
		return asset.ErrManagedExternally
	}
	if cur.Source == SourceAuto {
		if !cur.Missing {
			return asset.ErrManagedExternally
		}
		if !isOwner {
			return asset.ErrForbidden
		}
	}
	return s.store.Delete(ctx, workspace, id)
}

// ApplyPostgresTable indexes a format: "postgres" dataset from a database.table.created or
// database.table.updated event (ADR 0116). It reports whether the event changed anything:
// false means it was stale (older than an event already applied), normal under at-least-once,
// out-of-order delivery and not an error.
func (s *Service) ApplyPostgresTable(ctx context.Context, u PostgresTableUpsert) (bool, error) {
	u, err := u.Normalize()
	if err != nil {
		return false, err
	}
	u.NewID, u.ReceivedAt = asset.NewID(), s.now()
	return s.store.ApplyPostgresTable(ctx, u)
}

// RemovePostgresTable marks a format: "postgres" dataset Missing from a database.table.deleted
// event. at is the event's publishedAt. Unlike RemoveTable, the row stays fully visible — see
// Store.RemovePostgresTable's doc comment.
func (s *Service) RemovePostgresTable(ctx context.Context, workspace, schema, name string, at time.Time) (bool, error) {
	if !asset.ValidWorkspace(workspace) {
		return false, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	schema, err := asset.Text("schema", schema, maxPgIdent, true, false)
	if err != nil {
		return false, err
	}
	name, err = asset.Text("name", name, maxTableName, true, false)
	if err != nil {
		return false, err
	}
	if at.IsZero() {
		return false, asset.Invalid("publishedAt", "is required")
	}
	return s.store.RemovePostgresTable(ctx, workspace, schema, name, at.UTC().Truncate(time.Microsecond))
}

// ApplyTable indexes an Iceberg table from a table.created/table.updated event (ADR 0085). It
// reports whether the event changed anything: false means it was stale (older than an event
// already applied), normal under at-least-once, out-of-order delivery and not an error.
func (s *Service) ApplyTable(ctx context.Context, u TableUpsert) (bool, error) {
	u, err := u.Normalize()
	if err != nil {
		return false, err
	}
	u.NewID, u.ReceivedAt = asset.NewID(), s.now()
	return s.store.ApplyTable(ctx, u)
}

// RemoveTable tombstones an Iceberg table from a table.deleted event. at is the event's
// publishedAt.
func (s *Service) RemoveTable(ctx context.Context, workspace, sourceModule, tableUUID string, at time.Time) (bool, error) {
	if !asset.ValidWorkspace(workspace) {
		return false, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	if !moduleRE.MatchString(sourceModule) {
		return false, asset.Invalid("publishedBy", "must be a module ID such as \"lakehouse\"")
	}
	tableUUID, err := asset.Text("tableUuid", tableUUID, maxUUID, true, false)
	if err != nil {
		return false, err
	}
	if at.IsZero() {
		return false, asset.Invalid("publishedAt", "is required")
	}
	return s.store.RemoveTable(ctx, workspace, sourceModule, tableUUID, at.UTC().Truncate(time.Microsecond))
}

// List filters tags through the same normalization registration uses, so a filter for
// "PII" finds datasets tagged "pii".
func (s *Service) List(ctx context.Context, workspace string, f ListFilter) (asset.Page[Dataset], error) {
	if len(f.Tags) > 0 {
		tags, err := normalizeTags(f.Tags)
		if err != nil {
			return asset.Page[Dataset]{}, err
		}
		f.Tags = tags
	}
	return s.store.List(ctx, workspace, f)
}

func (s *Service) Tags(ctx context.Context, workspace string) ([]TagCount, error) {
	return s.store.Tags(ctx, workspace)
}

// ContainingLocation finds the datasets registered at, or as an ancestor folder of, loc.
func (s *Service) ContainingLocation(ctx context.Context, workspace string, loc asset.Location) ([]Dataset, error) {
	loc, err := asset.NormalizeLocation("location", loc)
	if err != nil {
		return nil, err
	}
	return s.store.ContainingLocation(ctx, workspace, loc)
}

func (s *Service) Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error) {
	return s.store.Search(ctx, workspace, query, limit)
}

func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }
