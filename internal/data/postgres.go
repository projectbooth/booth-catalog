package data

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	if err := db.Migrate(ctx, pool, "data", migrationFS, "migrations"); err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

const selectCols = `id, workspace, name, description, backend_id, path, columns, tags, owner, created_by, created_at, updated_at`

// searchable is the text a query is matched against: name, description and the tags joined
// into one string (so a query term matches inside any tag).
var searchable = []string{"name", "description", "array_to_string(tags, ' ')"}

func scan(row pgx.Row) (Dataset, error) {
	var d Dataset
	if err := row.Scan(&d.ID, &d.Workspace, &d.Name, &d.Description, &d.Location.BackendID, &d.Location.Path,
		&d.Schema, &d.Tags, &d.Owner, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return Dataset{}, err
	}
	if d.Schema == nil {
		d.Schema = []Column{}
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	return d, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// nonNil makes a nil slice an empty one: pgx sends a nil slice as SQL NULL, which the NOT NULL
// columns refuse. The service always normalizes, but the store shouldn't depend on that.
func nonNil(d Dataset) Dataset {
	if d.Schema == nil {
		d.Schema = []Column{}
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	return d
}

func (s *PostgresStore) Create(ctx context.Context, d Dataset) error {
	d = nonNil(d)
	_, err := s.pool.Exec(ctx, `INSERT INTO datasets
		(id, workspace, name, description, backend_id, path, columns, tags, owner, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		d.ID, d.Workspace, d.Name, d.Description, d.Location.BackendID, d.Location.Path,
		d.Schema, d.Tags, d.Owner, d.CreatedBy, d.CreatedAt, d.UpdatedAt)
	if isUniqueViolation(err) {
		return asset.ErrExists
	}
	if err != nil {
		return fmt.Errorf("inserting dataset: %w", err)
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, ws, id string) (Dataset, error) {
	d, err := scan(s.pool.QueryRow(ctx, `SELECT `+selectCols+` FROM datasets WHERE workspace = $1 AND id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Dataset{}, asset.ErrNotFound
	}
	if err != nil {
		return Dataset{}, fmt.Errorf("reading dataset: %w", err)
	}
	return d, nil
}

func (s *PostgresStore) Update(ctx context.Context, d Dataset) error {
	d = nonNil(d)
	tag, err := s.pool.Exec(ctx, `UPDATE datasets SET
		name = $3, description = $4, backend_id = $5, path = $6, columns = $7, tags = $8, owner = $9, updated_at = $10
		WHERE workspace = $1 AND id = $2`,
		d.Workspace, d.ID, d.Name, d.Description, d.Location.BackendID, d.Location.Path, d.Schema, d.Tags, d.Owner, d.UpdatedAt)
	if isUniqueViolation(err) {
		return asset.ErrExists
	}
	if err != nil {
		return fmt.Errorf("updating dataset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return asset.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) Delete(ctx context.Context, ws, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM datasets WHERE workspace = $1 AND id = $2`, ws, id)
	if err != nil {
		return fmt.Errorf("deleting dataset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return asset.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) List(ctx context.Context, ws string, f ListFilter) (asset.Page[Dataset], error) {
	where := []string{"workspace = $1"}
	args := []any{ws}

	text, textArgs := db.TextCondition(asset.Terms(f.Query), searchable, len(args)+1)
	where = append(where, text)
	args = append(args, textArgs...)

	if len(f.Tags) > 0 {
		args = append(args, f.Tags)
		where = append(where, "tags @> $"+strconv.Itoa(len(args))+"::text[]")
	}
	if f.Owner != "" {
		args = append(args, f.Owner)
		where = append(where, "lower(owner) = lower($"+strconv.Itoa(len(args))+")")
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM datasets WHERE `+cond, args...).Scan(&total); err != nil {
		return asset.Page[Dataset]{}, fmt.Errorf("counting datasets: %w", err)
	}

	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, asset.ClampLimit(f.Limit), offset)
	rows, err := s.pool.Query(ctx, `SELECT `+selectCols+` FROM datasets WHERE `+cond+
		` ORDER BY lower(name) COLLATE "C", id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return asset.Page[Dataset]{}, fmt.Errorf("listing datasets: %w", err)
	}
	items, err := collect(rows)
	if err != nil {
		return asset.Page[Dataset]{}, err
	}
	return asset.Page[Dataset]{Items: items, Total: total}, nil
}

func collect(rows pgx.Rows) ([]Dataset, error) {
	defer rows.Close()
	out := []Dataset{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning dataset: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ContainingLocation(ctx context.Context, ws string, loc asset.Location) ([]Dataset, error) {
	// A dataset contains loc when its path is '' (the backend root), equals loc's path, or is
	// a "/"-boundary ancestor of it. starts_with rather than LIKE so that a '%' or '_' in a
	// stored path is compared literally — the SQL twin of asset.PathContains.
	rows, err := s.pool.Query(ctx, `SELECT `+selectCols+` FROM datasets
		WHERE workspace = $1 AND backend_id = $2
		  AND (path = '' OR path = $3 OR starts_with($3, path || '/'))
		ORDER BY lower(name) COLLATE "C", id`, ws, loc.BackendID, loc.Path)
	if err != nil {
		return nil, fmt.Errorf("finding datasets by location: %w", err)
	}
	return collect(rows)
}

func (s *PostgresStore) Tags(ctx context.Context, ws string) ([]TagCount, error) {
	rows, err := s.pool.Query(ctx, `SELECT tag, count(*) FROM datasets, unnest(tags) AS tag
		WHERE workspace = $1 GROUP BY tag ORDER BY tag`, ws)
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Search(ctx context.Context, ws, query string, limit int) ([]asset.Hit, error) {
	terms := asset.Terms(query)
	if len(terms) == 0 {
		return []asset.Hit{}, nil
	}
	args := []any{ws}
	text, textArgs := db.TextCondition(terms, searchable, len(args)+1)
	args = append(args, textArgs...)
	// Whether the name alone satisfies the query — reuses the same term placeholders.
	nameMatch, _ := db.TextCondition(terms, []string{"name"}, 2)

	if limit <= 0 {
		limit = asset.DefaultLimit
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, owner, `+nameMatch+` AS name_match
		FROM datasets WHERE workspace = $1 AND `+text+`
		ORDER BY name_match DESC, lower(name) COLLATE "C", id LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("searching datasets: %w", err)
	}
	defer rows.Close()
	hits := []asset.Hit{}
	for rows.Next() {
		h := asset.Hit{Type: asset.TypeData}
		if err := rows.Scan(&h.ID, &h.Name, &h.Description, &h.Owner, &h.NameMatch); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
