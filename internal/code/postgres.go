package code

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	if err := db.Migrate(ctx, pool, "code", migrationFS, "migrations"); err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

var searchable = []string{"name", "description"}

// entrySelect reads an entry with its derived fields: the version count and the summary of
// the newest version (highest seq). The latest-version columns are nullable only because
// it's a LEFT JOIN — an entry always has at least one version.
const entrySelect = `SELECT e.id, e.workspace, e.name, e.description, e.owner, e.language, e.created_by, e.created_at, e.updated_at,
		(SELECT count(*) FROM code_versions c WHERE c.workspace = e.workspace AND c.entry_id = e.id),
		lv.version, lv.seq, lv.notes, octet_length(lv.source), lv.published_by, lv.published_at
	FROM code_entries e
	LEFT JOIN LATERAL (
		SELECT * FROM code_versions v WHERE v.workspace = e.workspace AND v.entry_id = e.id ORDER BY v.seq DESC LIMIT 1
	) lv ON TRUE`

func scanEntry(row pgx.Row) (Entry, error) {
	var (
		e           Entry
		version     *string
		seq, size   *int
		notes, by   *string
		publishedAt *time.Time
	)
	if err := row.Scan(&e.ID, &e.Workspace, &e.Name, &e.Description, &e.Owner, &e.Language, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt,
		&e.VersionCount, &version, &seq, &notes, &size, &by, &publishedAt); err != nil {
		return Entry{}, err
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	if version != nil {
		e.LatestVersion = &VersionSummary{
			Version: *version, Seq: *seq, Notes: *notes, SizeBytes: *size, PublishedBy: *by, PublishedAt: publishedAt.UTC(),
		}
	}
	return e, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresStore) Create(ctx context.Context, e Entry, first Version) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	_, err = tx.Exec(ctx, `INSERT INTO code_entries (id, workspace, name, description, owner, language, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.ID, e.Workspace, e.Name, e.Description, e.Owner, e.Language, e.CreatedBy, e.CreatedAt, e.UpdatedAt)
	if isUniqueViolation(err) {
		return asset.ErrExists
	}
	if err != nil {
		return fmt.Errorf("inserting code entry: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO code_versions (workspace, entry_id, version, seq, notes, source, published_by, published_at)
		VALUES ($1,$2,$3,1,$4,$5,$6,$7)`,
		e.Workspace, e.ID, first.Version, first.Notes, first.Source, first.PublishedBy, first.PublishedAt); err != nil {
		return fmt.Errorf("inserting first version: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Get(ctx context.Context, ws, id string) (Entry, error) {
	e, err := scanEntry(s.pool.QueryRow(ctx, entrySelect+` WHERE e.workspace = $1 AND e.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, asset.ErrNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("reading code entry: %w", err)
	}
	return e, nil
}

func (s *PostgresStore) Update(ctx context.Context, e Entry) error {
	tag, err := s.pool.Exec(ctx, `UPDATE code_entries SET name = $3, description = $4, owner = $5, language = $6, updated_at = $7
		WHERE workspace = $1 AND id = $2`, e.Workspace, e.ID, e.Name, e.Description, e.Owner, e.Language, e.UpdatedAt)
	if isUniqueViolation(err) {
		return asset.ErrExists
	}
	if err != nil {
		return fmt.Errorf("updating code entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return asset.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) Delete(ctx context.Context, ws, id string) error {
	// Versions go with it (ON DELETE CASCADE).
	tag, err := s.pool.Exec(ctx, `DELETE FROM code_entries WHERE workspace = $1 AND id = $2`, ws, id)
	if err != nil {
		return fmt.Errorf("deleting code entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return asset.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) List(ctx context.Context, ws string, f ListFilter) (asset.Page[Entry], error) {
	where := []string{"e.workspace = $1"}
	args := []any{ws}

	// TextCondition writes bare column names; qualify them for the aliased table.
	qualified := make([]string, len(searchable))
	for i, c := range searchable {
		qualified[i] = "e." + c
	}
	text, textArgs := db.TextCondition(asset.Terms(f.Query), qualified, len(args)+1)
	where = append(where, text)
	args = append(args, textArgs...)

	if f.Owner != "" {
		args = append(args, f.Owner)
		where = append(where, "lower(e.owner) = lower($"+strconv.Itoa(len(args))+")")
	}
	if f.Language != "" {
		args = append(args, f.Language)
		where = append(where, "e.language = $"+strconv.Itoa(len(args)))
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM code_entries e WHERE `+cond, args...).Scan(&total); err != nil {
		return asset.Page[Entry]{}, fmt.Errorf("counting code entries: %w", err)
	}

	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, asset.ClampLimit(f.Limit), offset)
	rows, err := s.pool.Query(ctx, entrySelect+` WHERE `+cond+
		` ORDER BY lower(e.name) COLLATE "C", e.id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return asset.Page[Entry]{}, fmt.Errorf("listing code entries: %w", err)
	}
	defer rows.Close()
	items := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return asset.Page[Entry]{}, fmt.Errorf("scanning code entry: %w", err)
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return asset.Page[Entry]{}, err
	}
	return asset.Page[Entry]{Items: items, Total: total}, nil
}

func (s *PostgresStore) AddVersion(ctx context.Context, ws, entryID string, v Version) (Version, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Lock the entry row: it serializes concurrent publishers so each gets a distinct,
	// gap-free seq, and doubles as the existence check.
	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM code_entries WHERE workspace = $1 AND id = $2 FOR UPDATE`, ws, entryID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, asset.ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("locking code entry: %w", err)
	}

	err = tx.QueryRow(ctx, `INSERT INTO code_versions (workspace, entry_id, version, seq, notes, source, published_by, published_at)
		SELECT $1, $2, $3, COALESCE(MAX(seq), 0) + 1, $4, $5, $6, $7 FROM code_versions WHERE workspace = $1 AND entry_id = $2
		RETURNING seq`, ws, entryID, v.Version, v.Notes, v.Source, v.PublishedBy, v.PublishedAt).Scan(&v.Seq)
	if isUniqueViolation(err) {
		return Version{}, asset.ErrExists
	}
	if err != nil {
		return Version{}, fmt.Errorf("inserting version: %w", err)
	}
	v.SizeBytes = len(v.Source)

	if _, err := tx.Exec(ctx, `UPDATE code_entries SET updated_at = $3 WHERE workspace = $1 AND id = $2`, ws, entryID, v.PublishedAt); err != nil {
		return Version{}, fmt.Errorf("touching code entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Version{}, fmt.Errorf("committing version: %w", err)
	}
	return v, nil
}

func (s *PostgresStore) Versions(ctx context.Context, ws, entryID string) ([]VersionSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT version, seq, notes, octet_length(source), published_by, published_at
		FROM code_versions WHERE workspace = $1 AND entry_id = $2 ORDER BY seq DESC`, ws, entryID)
	if err != nil {
		return nil, fmt.Errorf("listing versions: %w", err)
	}
	defer rows.Close()
	out := []VersionSummary{}
	for rows.Next() {
		var v VersionSummary
		if err := rows.Scan(&v.Version, &v.Seq, &v.Notes, &v.SizeBytes, &v.PublishedBy, &v.PublishedAt); err != nil {
			return nil, err
		}
		v.PublishedAt = v.PublishedAt.UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// An entry always has a version, so an empty history means the entry isn't there.
		var one int
		err := s.pool.QueryRow(ctx, `SELECT 1 FROM code_entries WHERE workspace = $1 AND id = $2`, ws, entryID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, asset.ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("checking code entry: %w", err)
		}
	}
	return out, nil
}

func (s *PostgresStore) GetVersion(ctx context.Context, ws, entryID, version string) (Version, error) {
	var v Version
	err := s.pool.QueryRow(ctx, `SELECT version, seq, notes, octet_length(source), published_by, published_at, source
		FROM code_versions WHERE workspace = $1 AND entry_id = $2 AND version = $3`, ws, entryID, version).
		Scan(&v.Version, &v.Seq, &v.Notes, &v.SizeBytes, &v.PublishedBy, &v.PublishedAt, &v.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, asset.ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("reading version: %w", err)
	}
	v.PublishedAt = v.PublishedAt.UTC()
	return v, nil
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
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, owner, `+nameMatch+` AS name_match
		FROM code_entries WHERE workspace = $1 AND `+text+`
		ORDER BY name_match DESC, lower(name) COLLATE "C", id LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("searching code entries: %w", err)
	}
	defer rows.Close()
	hits := []asset.Hit{}
	for rows.Next() {
		h := asset.Hit{Type: asset.TypeCode}
		if err := rows.Scan(&h.ID, &h.Name, &h.Description, &h.Owner, &h.NameMatch); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
