// This file is booth-catalog's other event consumer: table.created / table.updated /
// table.deleted from booth-lakehouse (ADR 0085), applied to the dataset catalog as
// format: "iceberg" rows. It is deliberately parallel to processor.go's dashboard.* handling
// rather than merged into it — a second, independent subscription (nats.go's Subscriber is
// reused with a different SubjectFilter/Consumer) with its own failure domain: a bug or outage
// in one event family never blocks the other.
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

// Event types this module consumes (ADR 0085).
const (
	EventTableCreated = "table.created"
	EventTableUpdated = "table.updated"
	EventTableDeleted = "table.deleted"
)

// SubjectFilterTables is the JetStream consumer's filter for table.* events, the ADR 0026
// subject shape mirrored from SubjectFilter's dashboard.* equivalent.
const SubjectFilterTables = "booth.*.table.*"

// TableCatalog is the part of the dataset service the TableProcessor drives.
type TableCatalog interface {
	ApplyTable(ctx context.Context, u data.TableUpsert) (bool, error)
	RemoveTable(ctx context.Context, workspace, sourceModule, tableUUID string, at time.Time) (bool, error)
}

// TableData is the type-specific payload of table.created and table.updated: the table
// summary ADR 0085 describes, minus snapshots (booth-lakehouse's full snapshot history isn't
// duplicated here). table.deleted carries only TableUUID. Unknown fields are ignored, so
// booth-lakehouse can add fields (formatVersion, schemaId, partitionSpec, lastUpdatedMs, ...)
// without breaking this consumer, exactly as DashboardData tolerates unused fields.
type TableData struct {
	Namespace         string         `json:"namespace"`
	Name              string         `json:"name"`
	TableUUID         string         `json:"tableUuid"`
	Location          asset.Location `json:"location"`
	Schema            []data.Column  `json:"schema"`
	CurrentSnapshotID *int64         `json:"currentSnapshotId"`
}

// TableProcessor applies table events to the dataset catalog. It is Processor's twin: same
// envelope handling and Ack/Retry/Drop verdicts, over a different subject shape and payload.
type TableProcessor struct {
	catalog TableCatalog
}

func NewTableProcessor(catalog TableCatalog) *TableProcessor {
	return &TableProcessor{catalog: catalog}
}

// parseTableSubject splits booth.<workspace>.table.<verb> and returns the workspace and the
// event type ("table.created" ...) — parseSubject's twin for the "table" event family.
func parseTableSubject(subject string) (workspace, eventType string, ok bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 || parts[0] != "booth" || parts[2] != "table" {
		return "", "", false
	}
	switch et := "table." + parts[3]; et {
	case EventTableCreated, EventTableUpdated, EventTableDeleted:
		return parts[1], et, true
	}
	return "", "", false
}

// Handle processes one message: its NATS subject and raw payload. See Processor.Handle for
// the reasoning behind checking the envelope against the subject rather than trusting either
// alone — identical here.
func (p *TableProcessor) Handle(ctx context.Context, subject string, payload []byte) Result {
	workspace, eventType, ok := parseTableSubject(subject)
	if !ok {
		return Result{Action: Drop, Reason: fmt.Sprintf("subject %q is not a table lifecycle event", subject)}
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

	var d TableData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return Result{Action: Drop, Reason: "malformed " + eventType + " data: " + err.Error()}
	}

	var (
		applied bool
		err     error
	)
	if eventType == EventTableDeleted {
		applied, err = p.catalog.RemoveTable(ctx, workspace, env.PublishedBy, d.TableUUID, env.PublishedAt)
	} else {
		applied, err = p.catalog.ApplyTable(ctx, data.TableUpsert{
			Workspace: workspace, SourceModule: env.PublishedBy,
			Namespace: d.Namespace, Name: d.Name, UUID: d.TableUUID,
			Location: d.Location, Schema: d.Schema, CurrentSnapshotID: d.CurrentSnapshotID,
			At: env.PublishedAt,
		})
	}

	var invalid *asset.ValidationError
	switch {
	case errors.As(err, &invalid):
		return Result{Action: Drop, Reason: fmt.Sprintf("invalid %s from %q: %v", eventType, env.PublishedBy, invalid)}
	case errors.Is(err, asset.ErrExists):
		// The table's computed namespace.name collides with an unrelated existing dataset.
		// Nothing about retrying changes that; it needs a human to rename one of them.
		return Result{Action: Drop, Reason: fmt.Sprintf("%s from %q: computed name collides with an existing, unrelated dataset", eventType, env.PublishedBy)}
	case err != nil:
		return Result{Action: Retry, Reason: err.Error()}
	case !applied:
		return Result{Action: Ack, Reason: "stale: an event for this table published later was already applied"}
	}
	return Result{Action: Ack, Applied: true}
}
