// Package db is the thin PostgreSQL layer shared by booth-catalog's asset-type packages:
// opening a pool against this module's own database on the shared cluster (ADR 0014), and
// applying each package's embedded migrations.
//
// Every asset package (data, code, dashboards) embeds its own migrations/ directory and
// migrates under its own component name. That keeps each package's schema next to its code,
// so lifting one out into its own repo later (ADR 0001) takes its tables with it.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

// migrationLockID is an arbitrary constant identifying booth-catalog's migration lock in
// Postgres's advisory-lock keyspace, so replicas starting at the same time serialize
// instead of racing to create the same table.
const migrationLockID int64 = 0x626f6f7463617461 // "bootcata"

// Open connects to dsn and verifies the connection.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return pool, nil
}

// Migrate applies the component's pending migrations from dir within fsys, in filename
// order, each exactly once. Filenames must start with a numeric version ("0001_x.sql").
// It runs in one transaction holding an advisory lock, so it is safe to call from every
// replica at boot.
func Migrate(ctx context.Context, pool *pgxpool.Pool, component string, fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("reading %s migrations: %w", component, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting %s migration transaction: %w", component, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("taking migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS catalog_schema_migrations (
		component  TEXT        NOT NULL,
		version    INT         NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (component, version)
	)`); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("%s migration %q: filename must start with a numeric version", component, name)
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_schema_migrations WHERE component = $1 AND version = $2)`, component, version).Scan(&applied); err != nil {
			return fmt.Errorf("checking %s migration %s: %w", component, name, err)
		}
		if applied {
			continue
		}
		sql, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("applying %s migration %s: %w", component, name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO catalog_schema_migrations (component, version) VALUES ($1, $2)`, component, version); err != nil {
			return fmt.Errorf("recording %s migration %s: %w", component, name, err)
		}
	}
	return tx.Commit(ctx)
}

// TextCondition builds a SQL boolean expression that is true when every term occurs
// (case-insensitively, as a literal substring) in at least one of exprs — the SQL twin of
// asset.MatchesAll, so the Postgres and in-memory stores agree on what a search matches.
// Placeholders are numbered from firstArg; the returned args slot straight into the query's
// argument list. With no terms it returns "TRUE".
//
// exprs are SQL expressions written by the calling package (column names), never user
// input; only the terms travel as parameters.
func TextCondition(terms []string, exprs []string, firstArg int) (string, []any) {
	if len(terms) == 0 {
		return "TRUE", nil
	}
	parts := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms))
	for i, term := range terms {
		ph := "$" + strconv.Itoa(firstArg+i)
		alts := make([]string, len(exprs))
		for j, e := range exprs {
			alts[j] = e + " ILIKE " + ph
		}
		parts = append(parts, "("+strings.Join(alts, " OR ")+")")
		args = append(args, asset.LikePattern(term))
	}
	return strings.Join(parts, " AND "), args
}
