package dashboards

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/db"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// PostgresStore is the Store backed by the platform's shared PostgreSQL cluster (ADR 0014).
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore applies this package's migrations and returns a store over pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if err := db.Migrate(ctx, pool, "dashboards", migrationFS, "migrations"); err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

var searchable = []string{"name", "description"}

const selectCols = `id, workspace, source_module, external_id, name, description, owner, path, lineage_complete, created_at, updated_at`

func scan(row pgx.Row) (Dashboard, error) {
	var d Dashboard
	if err := row.Scan(&d.ID, &d.Workspace, &d.SourceModule, &d.ExternalID, &d.Name, &d.Description, &d.Owner, &d.Path,
		&d.LineageComplete, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return Dashboard{}, err
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	return d, nil
}

func (s *PostgresStore) Apply(ctx context.Context, u Upsert) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// One atomic statement decides staleness and takes the row lock: the WHERE on the
	// conflict branch means an event older than what's applied returns no row. Concurrent
	// events for one dashboard (several replicas consuming) queue on that row lock.
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO dashboards
		(id, workspace, source_module, external_id, name, description, owner, path, lineage_complete, created_at, updated_at, deleted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULL)
		ON CONFLICT (workspace, source_module, external_id) DO UPDATE SET
			name = EXCLUDED.name, description = EXCLUDED.description, owner = EXCLUDED.owner, path = EXCLUDED.path,
			lineage_complete = EXCLUDED.lineage_complete, updated_at = EXCLUDED.updated_at,
			-- Reviving a tombstone starts a new life: it is "first indexed" again now.
			created_at = CASE WHEN dashboards.deleted_at IS NOT NULL THEN EXCLUDED.created_at ELSE dashboards.created_at END,
			deleted_at = NULL
		WHERE dashboards.updated_at <= EXCLUDED.updated_at
		RETURNING id`,
		u.NewID, u.Workspace, u.SourceModule, u.ExternalID, u.Name, u.Description, u.Owner, u.Path, u.LineageComplete, u.ReceivedAt, u.At).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // stale
	}
	if err != nil {
		return false, fmt.Errorf("upserting dashboard: %w", err)
	}

	if err := replaceSources(ctx, tx, u.Workspace, id, u.Sources); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing dashboard: %w", err)
	}
	return true, nil
}

func replaceSources(ctx context.Context, tx pgx.Tx, ws, id string, sources []Source) error {
	if _, err := tx.Exec(ctx, `DELETE FROM dashboard_sources WHERE workspace = $1 AND dashboard_id = $2`, ws, id); err != nil {
		return fmt.Errorf("clearing dashboard sources: %w", err)
	}
	if len(sources) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i, s := range sources {
		batch.Queue(`INSERT INTO dashboard_sources (workspace, dashboard_id, ordinal, type, dataset_id, backend_id, path, system, name)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ws, id, i, string(s.Type), s.DatasetID, s.BackendID, s.Path, s.System, s.Name)
	}
	res := tx.SendBatch(ctx, batch)
	defer res.Close()
	for range sources {
		if _, err := res.Exec(); err != nil {
			return fmt.Errorf("inserting dashboard source: %w", err)
		}
	}
	return res.Close()
}

func (s *PostgresStore) Remove(ctx context.Context, ws, module, externalID string, at time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var id string
	err = tx.QueryRow(ctx, `INSERT INTO dashboards (id, workspace, source_module, external_id, created_at, updated_at, deleted_at)
		VALUES ($1,$2,$3,$4,$5,$5,$5)
		ON CONFLICT (workspace, source_module, external_id) DO UPDATE SET updated_at = EXCLUDED.updated_at, deleted_at = EXCLUDED.deleted_at
		WHERE dashboards.updated_at <= EXCLUDED.updated_at
		RETURNING id`, asset.NewID(), ws, module, externalID, at).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // stale
	}
	if err != nil {
		return false, fmt.Errorf("tombstoning dashboard: %w", err)
	}
	if err := replaceSources(ctx, tx, ws, id, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing tombstone: %w", err)
	}
	return true, nil
}

func (s *PostgresStore) Get(ctx context.Context, ws, id string) (Dashboard, error) {
	d, err := scan(s.pool.QueryRow(ctx, `SELECT `+selectCols+` FROM dashboards WHERE workspace = $1 AND id = $2 AND deleted_at IS NULL`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Dashboard{}, asset.ErrNotFound
	}
	if err != nil {
		return Dashboard{}, fmt.Errorf("reading dashboard: %w", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT type, dataset_id, backend_id, path, system, name
		FROM dashboard_sources WHERE workspace = $1 AND dashboard_id = $2 ORDER BY ordinal`, ws, id)
	if err != nil {
		return Dashboard{}, fmt.Errorf("reading dashboard sources: %w", err)
	}
	defer rows.Close()
	d.Sources = []Source{}
	for rows.Next() {
		var src Source
		var typ string
		if err := rows.Scan(&typ, &src.DatasetID, &src.BackendID, &src.Path, &src.System, &src.Name); err != nil {
			return Dashboard{}, err
		}
		src.Type = SourceType(typ)
		d.Sources = append(d.Sources, src)
	}
	return d, rows.Err()
}

func collect(rows pgx.Rows) ([]Dashboard, error) {
	defer rows.Close()
	out := []Dashboard{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning dashboard: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) List(ctx context.Context, ws string, f ListFilter) (asset.Page[Dashboard], error) {
	where := []string{"workspace = $1", "deleted_at IS NULL"}
	args := []any{ws}

	text, textArgs := db.TextCondition(asset.Terms(f.Query), searchable, len(args)+1)
	where = append(where, text)
	args = append(args, textArgs...)

	if f.Owner != "" {
		args = append(args, f.Owner)
		where = append(where, "lower(owner) = lower($"+strconv.Itoa(len(args))+")")
	}
	if f.SourceModule != "" {
		args = append(args, f.SourceModule)
		where = append(where, "source_module = $"+strconv.Itoa(len(args)))
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM dashboards WHERE `+cond, args...).Scan(&total); err != nil {
		return asset.Page[Dashboard]{}, fmt.Errorf("counting dashboards: %w", err)
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, asset.ClampLimit(f.Limit), offset)
	rows, err := s.pool.Query(ctx, `SELECT `+selectCols+` FROM dashboards WHERE `+cond+
		` ORDER BY lower(name) COLLATE "C", id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return asset.Page[Dashboard]{}, fmt.Errorf("listing dashboards: %w", err)
	}
	items, err := collect(rows)
	if err != nil {
		return asset.Page[Dashboard]{}, err
	}
	return asset.Page[Dashboard]{Items: items, Total: total}, nil
}

func (s *PostgresStore) ReadingDataset(ctx context.Context, ws, datasetID string, loc asset.Location) ([]Dashboard, error) {
	// A location source is read "within" the dataset when the dataset's path equals it,
	// is a "/"-boundary ancestor of it, or is '' (the backend root) — the SQL twin of
	// asset.PathContains. starts_with, not LIKE, so a '%' or '_' in a path is literal.
	rows, err := s.pool.Query(ctx, `SELECT `+selectCols+` FROM dashboards d
		WHERE d.workspace = $1 AND d.deleted_at IS NULL AND EXISTS (
			SELECT 1 FROM dashboard_sources s WHERE s.workspace = d.workspace AND s.dashboard_id = d.id AND (
				(s.type = 'dataset' AND s.dataset_id = $2)
				OR (s.type = 'location' AND s.backend_id = $3
				    AND ($4::text = '' OR s.path = $4::text OR starts_with(s.path, $4::text || '/')))
			))
		ORDER BY lower(d.name) COLLATE "C", d.id`, ws, datasetID, loc.BackendID, loc.Path)
	if err != nil {
		return nil, fmt.Errorf("finding dashboards reading a dataset: %w", err)
	}
	return collect(rows)
}

func (s *PostgresStore) Search(ctx context.Context, ws, query string, limit int) ([]asset.Hit, error) {
	terms := asset.Terms(query)
	if len(terms) == 0 {
		return []asset.Hit{}, nil
	}
	args := []any{ws}
	text, textArgs := db.TextCondition(terms, searchable, len(args)+1)
	args = append(args, textArgs...)
	nameMatch, _ := db.TextCondition(terms, []string{"name"}, 2)

	if limit <= 0 {
		limit = asset.DefaultLimit
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, owner, source_module, `+nameMatch+` AS name_match
		FROM dashboards WHERE workspace = $1 AND deleted_at IS NULL AND `+text+`
		ORDER BY name_match DESC, lower(name) COLLATE "C", id LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("searching dashboards: %w", err)
	}
	defer rows.Close()
	hits := []asset.Hit{}
	for rows.Next() {
		h := asset.Hit{Type: asset.TypeDashboard}
		if err := rows.Scan(&h.ID, &h.Name, &h.Description, &h.Owner, &h.Source, &h.NameMatch); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
