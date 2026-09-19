package code

import (
	"context"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// Service is the code catalog's business logic: validation, ID/timestamp/sequence
// assignment and owner defaulting over a Store. Handlers call this, never the Store.
type Service struct {
	store     Store
	maxSource int
	now       func() time.Time
}

// NewService returns a Service. maxSourceBytes bounds one version's source; zero or negative
// means DefaultMaxSourceBytes.
func NewService(store Store, maxSourceBytes int) *Service {
	if maxSourceBytes <= 0 {
		maxSourceBytes = DefaultMaxSourceBytes
	}
	return &Service{store: store, maxSource: maxSourceBytes, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
}

// MaxSourceBytes reports the per-version source limit, so the API can advertise it.
func (s *Service) MaxSourceBytes() int { return s.maxSource }

// Create publishes a new entry with its first version. An empty owner defaults to the actor.
func (s *Service) Create(ctx context.Context, workspace string, actor asset.Actor, in CreateInput) (Entry, error) {
	if !asset.ValidWorkspace(workspace) {
		return Entry{}, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	meta, err := in.EntryInput.Normalize()
	if err != nil {
		return Entry{}, err
	}
	ver, err := in.VersionInput.Normalize(s.maxSource)
	if err != nil {
		return Entry{}, err
	}
	if meta.Owner == "" {
		meta.Owner = actor.DisplayName
	}
	now := s.now()
	e := Entry{
		ID: asset.NewID(), Workspace: workspace,
		Name: meta.Name, Description: meta.Description, Owner: meta.Owner, Language: meta.Language,
		CreatedBy: actor.Subject, CreatedAt: now, UpdatedAt: now,
	}
	first := Version{
		VersionSummary: VersionSummary{Version: ver.Version, Notes: ver.Notes, PublishedBy: actor.DisplayName, PublishedAt: now},
		Source:         ver.Source,
	}
	if err := s.store.Create(ctx, e, first); err != nil {
		return Entry{}, err
	}
	return s.store.Get(ctx, workspace, e.ID)
}

// Update replaces an entry's metadata. An empty owner keeps the current one rather than
// resetting it to the caller, for the same reason as the dataset catalog: tidying a
// description must not silently transfer ownership. Versions are untouched.
func (s *Service) Update(ctx context.Context, workspace, id string, in EntryInput) (Entry, error) {
	meta, err := in.Normalize()
	if err != nil {
		return Entry{}, err
	}
	cur, err := s.store.Get(ctx, workspace, id)
	if err != nil {
		return Entry{}, err
	}
	if meta.Owner == "" {
		meta.Owner = cur.Owner
	}
	cur.Name, cur.Description, cur.Owner, cur.Language = meta.Name, meta.Description, meta.Owner, meta.Language
	cur.UpdatedAt = s.now()
	if err := s.store.Update(ctx, cur); err != nil {
		return Entry{}, err
	}
	return s.store.Get(ctx, workspace, id)
}

func (s *Service) Get(ctx context.Context, workspace, id string) (Entry, error) {
	return s.store.Get(ctx, workspace, id)
}

// Delete removes an entry and every version of it.
func (s *Service) Delete(ctx context.Context, workspace, id string) error {
	return s.store.Delete(ctx, workspace, id)
}

func (s *Service) List(ctx context.Context, workspace string, f ListFilter) (asset.Page[Entry], error) {
	return s.store.List(ctx, workspace, f)
}

// Publish adds a new version to an entry. A version label that already exists is refused
// (asset.ErrExists): published versions are immutable, so a consumer that pinned "1.2.0"
// can rely on it meaning the same source tomorrow.
func (s *Service) Publish(ctx context.Context, workspace string, actor asset.Actor, entryID string, in VersionInput) (Version, error) {
	ver, err := in.Normalize(s.maxSource)
	if err != nil {
		return Version{}, err
	}
	return s.store.AddVersion(ctx, workspace, entryID, Version{
		VersionSummary: VersionSummary{Version: ver.Version, Notes: ver.Notes, PublishedBy: actor.DisplayName, PublishedAt: s.now()},
		Source:         ver.Source,
	})
}

// Versions returns an entry's history, newest first, without source.
func (s *Service) Versions(ctx context.Context, workspace, entryID string) ([]VersionSummary, error) {
	return s.store.Versions(ctx, workspace, entryID)
}

// GetVersion returns one version with its source. The label "latest" resolves to the most
// recently published version (highest Seq), which is why no version may be named that.
func (s *Service) GetVersion(ctx context.Context, workspace, entryID, version string) (Version, error) {
	if version == LatestVersion {
		e, err := s.store.Get(ctx, workspace, entryID)
		if err != nil {
			return Version{}, err
		}
		if e.LatestVersion == nil {
			return Version{}, asset.ErrNotFound
		}
		version = e.LatestVersion.Version
	}
	return s.store.GetVersion(ctx, workspace, entryID, version)
}

func (s *Service) Search(ctx context.Context, workspace, query string, limit int) ([]asset.Hit, error) {
	return s.store.Search(ctx, workspace, query, limit)
}

func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }
