// This file is booth-catalog's third event consumer: database.table.created /
// database.table.updated / database.table.deleted from booth-database (ADR 0116), applied to
// the dataset catalog as format: "postgres" rows. Deliberately parallel to tables.go's
// Iceberg handling rather than merged into it — its own Subscriber (nats.go's SubjectFilter
// and Consumer fields let several run side by side over one connection), its own durable
// consumer, and its own failure domain.
//
// The payload shape below is provisional (ADR 0116 §1): booth-database's session had not yet
// reported final field names when this was built, so this is built against
// hack/publish-database-table-event's fixtures rather than a real publisher, and every field
// not explicitly required is tolerated missing or of an unexpected shape rather than rejected
// — see DatabaseTableData's doc comment for exactly what's assumed.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/data"
)

// Event types this module consumes (ADR 0116).
const (
	EventDatabaseTableCreated = "database.table.created"
	EventDatabaseTableUpdated = "database.table.updated"
	EventDatabaseTableDeleted = "database.table.deleted"
)

// SubjectFilterDatabaseTables is the JetStream consumer's filter for database.table.* events:
// ADR 0026's booth.<workspace>.<event-type> with event-type itself carrying two dots
// ("database.table.created"), so the subject has five dot-separated tokens, not four like
// dashboard.* and table.*.
const SubjectFilterDatabaseTables = "booth.*.database.table.*"

// DatabaseTableCatalog is the part of the dataset service the DatabaseTableProcessor drives.
type DatabaseTableCatalog interface {
	ApplyPostgresTable(ctx context.Context, u data.PostgresTableUpsert) (bool, error)
	RemovePostgresTable(ctx context.Context, workspace, schema, name string, at time.Time) (bool, error)
}

// DatabaseTableData is the type-specific payload of database.table.created and
// database.table.updated (ADR 0116): schema, name, kind, columns and an approximate row
// count. database.table.deleted carries only Schema and Name.
//
// Provisional, per the ADR: booth-database hadn't reported final field names when this was
// built. Assumptions made here, to revisit once it has:
//   - Column's wire shape is {name, type, nullable, comment} — "comment" maps to data.Column's
//     existing Description field (the same concept under booth-lakehouse's and a hand-typed
//     schema's own naming), so no new JSON key is introduced for it.
//   - schemaHash is read by booth-database for its own change detection and never needed here
//     (last-writer-wins by publishedAt already orders events correctly) — it isn't a field
//     below, which already means it's silently ignored like any other unknown field.
//   - kind is treated as an opaque, non-empty string (data.TableKind), not validated against a
//     closed enum: the ADR names "table", "view" and "materialized view" today, but a value
//     this build doesn't recognize should still catalog and display rather than being dropped.
type DatabaseTableData struct {
	Schema     string             `json:"schema"`
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	Columns    []databaseTableCol `json:"columns"`
	ApproxRows *int64             `json:"approxRows"`
}

// databaseTableCol is one column on the wire (ADR 0116): "comment" rather than "description",
// matching booth-database's own vocabulary for a Postgres column/table comment.
type databaseTableCol struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Comment  string `json:"comment"`
}

func toColumns(cols []databaseTableCol) []data.Column {
	out := make([]data.Column, len(cols))
	for i, c := range cols {
		out[i] = data.Column{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Description: c.Comment}
	}
	return out
}

// DatabaseTableProcessor applies database.table.* events to the dataset catalog. Processor's
// and TableProcessor's twin: same envelope handling and Ack/Retry/Drop verdicts, over its own
// subject shape and payload.
type DatabaseTableProcessor struct {
	catalog DatabaseTableCatalog
}

func NewDatabaseTableProcessor(catalog DatabaseTableCatalog) *DatabaseTableProcessor {
	return &DatabaseTableProcessor{catalog: catalog}
}

// parseDatabaseTableSubject splits booth.<workspace>.database.table.<verb> and returns the
// workspace and the event type ("database.table.created" ...).
func parseDatabaseTableSubject(subject string) (workspace, eventType string, ok bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 5 || parts[0] != "booth" || parts[2] != "database" || parts[3] != "table" {
		return "", "", false
	}
	switch et := "database.table." + parts[4]; et {
	case EventDatabaseTableCreated, EventDatabaseTableUpdated, EventDatabaseTableDeleted:
		return parts[1], et, true
	}
	return "", "", false
}

// Handle processes one message: its NATS subject and raw payload. See Processor.Handle for
// why the envelope is checked against the subject rather than trusting either alone —
// identical here.
func (p *DatabaseTableProcessor) Handle(ctx context.Context, subject string, payload []byte) Result {
	workspace, eventType, ok := parseDatabaseTableSubject(subject)
	if !ok {
		return Result{Action: Drop, Reason: fmt.Sprintf("subject %q is not a database table lifecycle event", subject)}
	}
	if !asset.ValidWorkspace(workspace) {
		return Result{Action: Drop, Reason: fmt.Sprintf("subject %q carries an invalid workspace slug", subject)}
	}

	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return Result{Action: Drop, Reason: "malformed event envelope: " + err.Error()}
	}
	if env.Workspace != workspace {
		return Result{Action: Drop, Reason: fmt.Sprintf("envelope workspace %q does not match subject workspace %q", env.Workspace, workspace)}
	}
	if env.EventType != eventType {
		return Result{Action: Drop, Reason: fmt.Sprintf("envelope eventType %q does not match subject event type %q", env.EventType, eventType)}
	}

	var d DatabaseTableData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return Result{Action: Drop, Reason: "malformed " + eventType + " data: " + err.Error()}
	}

	var (
		applied bool
		err     error
	)
	if eventType == EventDatabaseTableDeleted {
		applied, err = p.catalog.RemovePostgresTable(ctx, workspace, d.Schema, d.Name, env.PublishedAt)
	} else {
		applied, err = p.catalog.ApplyPostgresTable(ctx, data.PostgresTableUpsert{
			Workspace: workspace, SourceModule: env.PublishedBy,
			Schema: d.Schema, Name: d.Name, Kind: data.TableKind(d.Kind),
			Columns: toColumns(d.Columns), ApproxRows: d.ApproxRows,
			At: env.PublishedAt,
		})
	}

	var invalid *asset.ValidationError
	switch {
	case errors.As(err, &invalid):
		return Result{Action: Drop, Reason: fmt.Sprintf("invalid %s from %q: %v", eventType, env.PublishedBy, invalid)}
	case errors.Is(err, asset.ErrExists):
		// The table's computed schema.name collides with an unrelated existing dataset.
		// Nothing about retrying changes that; it needs a human to rename one of them.
		return Result{Action: Drop, Reason: fmt.Sprintf("%s from %q: computed name collides with an existing, unrelated dataset", eventType, env.PublishedBy)}
	case err != nil:
		return Result{Action: Retry, Reason: err.Error()}
	case !applied:
		return Result{Action: Ack, Reason: "stale: an event for this table published later was already applied"}
	}
	return Result{Action: Ack, Applied: true}
}
