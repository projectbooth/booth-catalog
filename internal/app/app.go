// Package app assembles booth-catalog's three asset-type services and the cross-asset search
// into one set, and holds the only code that knows more than one of them: the small adapter
// that lets the dashboard catalog read the dataset catalog.
//
// Keeping that seam in one place is what preserves the folder boundary ADR 0001 asks for. The
// data, code and dashboards packages never import each other; if one is ever split into its
// own repo, this file is where the wiring changes (an in-process adapter becomes a call).
package app

import (
	"context"
	"errors"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/code"
	"github.com/projectbooth/booth-catalog/internal/dashboards"
	"github.com/projectbooth/booth-catalog/internal/data"
	"github.com/projectbooth/booth-catalog/internal/search"
)

// Services is the assembled catalog.
type Services struct {
	Data       *data.Service
	Code       *code.Service
	Dashboards *dashboards.Service
	Search     *search.Service
}

// Stores is the persistence behind Services: Postgres in production, memory in dev mode and
// tests.
type Stores struct {
	Data       data.Store
	Code       code.Store
	Dashboards dashboards.Store
}

// New wires services over stores. maxCodeSource bounds one code version's source in bytes
// (zero means the default).
func New(stores Stores, maxCodeSource int) *Services {
	ds := data.NewService(stores.Data)
	cs := code.NewService(stores.Code, maxCodeSource)
	bs := dashboards.NewService(stores.Dashboards, datasetResolver{ds})
	return &Services{
		Data: ds, Code: cs, Dashboards: bs,
		Search: search.New(map[asset.Type]search.Searcher{
			asset.TypeData: ds, asset.TypeCode: cs, asset.TypeDashboard: bs,
		}),
	}
}

// NewMemory wires services over in-memory stores: dev mode, and tests that don't need a
// database. Nothing survives a restart.
func NewMemory(maxCodeSource int) *Services {
	return New(Stores{Data: data.NewMemoryStore(), Code: code.NewMemoryStore(), Dashboards: dashboards.NewMemoryStore()}, maxCodeSource)
}

// Ping reports whether every store is reachable, for the health check.
func (s *Services) Ping(ctx context.Context) error {
	if err := s.Data.Ping(ctx); err != nil {
		return err
	}
	if err := s.Code.Ping(ctx); err != nil {
		return err
	}
	return s.Dashboards.Ping(ctx)
}

// datasetResolver adapts the dataset service to what the dashboard catalog needs to resolve
// lineage, without the dashboards package importing data.
type datasetResolver struct{ ds *data.Service }

func (r datasetResolver) ByID(ctx context.Context, workspace, id string) (dashboards.DatasetRef, bool, error) {
	d, err := r.ds.Get(ctx, workspace, id)
	if err != nil {
		if errors.Is(err, asset.ErrNotFound) {
			return dashboards.DatasetRef{}, false, nil
		}
		return dashboards.DatasetRef{}, false, err
	}
	return dashboards.DatasetRef{ID: d.ID, Name: d.Name}, true, nil
}

func (r datasetResolver) ByLocation(ctx context.Context, workspace string, loc asset.Location) ([]dashboards.DatasetRef, error) {
	found, err := r.ds.ContainingLocation(ctx, workspace, loc)
	if err != nil {
		return nil, err
	}
	out := make([]dashboards.DatasetRef, len(found))
	for i, d := range found {
		out[i] = dashboards.DatasetRef{ID: d.ID, Name: d.Name}
	}
	return out, nil
}
