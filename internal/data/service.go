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
	}
	if err := s.store.Create(ctx, d); err != nil {
		return Dataset{}, err
	}
	return d, nil
}

// Update replaces a dataset's mutable fields. An empty owner keeps the current one rather
// than resetting it to the caller: an editor tidying a description must not silently take
// ownership of someone else's dataset.
func (s *Service) Update(ctx context.Context, workspace, id string, in Input) (Dataset, error) {
	in, err := in.Normalize()
	if err != nil {
		return Dataset{}, err
	}
	cur, err := s.store.Get(ctx, workspace, id)
	if err != nil {
		return Dataset{}, err
	}
	if in.Owner == "" {
		in.Owner = cur.Owner
	}
	cur.Name, cur.Description, cur.Location = in.Name, in.Description, in.Location
	cur.Schema, cur.Tags, cur.Owner = in.Schema, in.Tags, in.Owner
	cur.UpdatedAt = s.now()
	if err := s.store.Update(ctx, cur); err != nil {
		return Dataset{}, err
	}
	return cur, nil
}

func (s *Service) Get(ctx context.Context, workspace, id string) (Dataset, error) {
	return s.store.Get(ctx, workspace, id)
}

func (s *Service) Delete(ctx context.Context, workspace, id string) error {
	return s.store.Delete(ctx, workspace, id)
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
