// Package events is booth-catalog's side of the event bus (ADR 0007, ADR 0021, ADR 0026):
// it consumes the dashboard.created / dashboard.updated / dashboard.deleted events that
// booth-superset, booth-metabase and booth-streamlit publish (ADR 0018) and applies them to
// the dashboard catalog.
//
// It is split in two so each half is testable on its own:
//
//   - Processor (this file) turns one received message — subject plus payload — into a
//     change to the dashboard catalog, and decides what should happen to the message. It
//     knows nothing about NATS, so it is tested with fabricated events: the "mocked event
//     stream" the brief asks for.
//   - Subscriber (nats.go) is the JetStream transport: a durable consumer that feeds messages
//     to the Processor and acks, retries or terminates them by its verdict. It is tested
//     against a real embedded nats-server.
//
// The payload shape implemented here is a proposal awaiting coordinator sign-off:
// docs/decisions/0001-dashboard-event-payload.md.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/dashboards"
)

// Event types this module consumes (ADR 0018).
const (
	EventDashboardCreated = "dashboard.created"
	EventDashboardUpdated = "dashboard.updated"
	EventDashboardDeleted = "dashboard.deleted"
)

// SubjectFilter is the JetStream consumer's filter: every workspace's dashboard lifecycle
// events (ADR 0026's booth.<workspace>.<event-type>, with dashboard.* being two tokens).
// One catalog serves every workspace, so it subscribes across them all and scopes each
// event by the workspace in its subject.
const SubjectFilter = "booth.*.dashboard.*"

// Action is what should happen to a message once the Processor has looked at it.
type Action int

const (
	// Ack: handled (applied, or deliberately ignored as stale) — never send it again.
	Ack Action = iota
	// Retry: a transient failure (the database is down, say) — redeliver later. The event
	// itself is fine.
	Retry
	// Drop: the event can never be applied (malformed, or not ours) — terminate it. Retrying
	// would loop forever on something that cannot succeed, and a message stuck redelivering
	// forever is a worse failure than a dropped bad event that is logged.
	Drop
)

func (a Action) String() string {
	return [...]string{"ack", "retry", "drop"}[a]
}

// Result is the Processor's verdict on one message.
type Result struct {
	Action Action
	// Reason explains a Retry or Drop, and notes an Ack that changed nothing (a stale
	// event), for the log. Empty for an ordinary applied event.
	Reason string
	// Applied reports whether the catalog changed. False for stale events and drops.
	Applied bool
}

// DashboardCatalog is the part of the dashboard service the Processor drives.
type DashboardCatalog interface {
	Apply(ctx context.Context, u dashboards.Upsert) (bool, error)
	Remove(ctx context.Context, workspace, sourceModule, externalID string, at time.Time) (bool, error)
}

// Envelope is the common wrapper every event is published in (ADR 0026).
type Envelope struct {
	Workspace   string          `json:"workspace"`
	EventType   string          `json:"eventType"`
	PublishedAt time.Time       `json:"publishedAt"`
	PublishedBy string          `json:"publishedBy"`
	Data        json.RawMessage `json:"data"`
}

// DashboardData is the type-specific payload of dashboard.created and dashboard.updated:
// the dashboard's full current state. dashboard.deleted carries only DashboardID. Unknown
// fields are ignored, so a publisher can add fields without breaking this consumer.
type DashboardData struct {
	DashboardID     string              `json:"dashboardId"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	Owner           string              `json:"owner"`
	Path            string              `json:"path"`
	LineageComplete bool                `json:"lineageComplete"`
	Sources         []dashboards.Source `json:"sources"`
}

// Processor applies dashboard events to the dashboard catalog.
type Processor struct {
	catalog DashboardCatalog
}

func NewProcessor(catalog DashboardCatalog) *Processor { return &Processor{catalog: catalog} }

// parseSubject splits booth.<workspace>.dashboard.<verb> and returns the workspace and the
// event type ("dashboard.created" ...).
func parseSubject(subject string) (workspace, eventType string, ok bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 || parts[0] != "booth" || parts[2] != "dashboard" {
		return "", "", false
	}
	switch et := "dashboard." + parts[3]; et {
	case EventDashboardCreated, EventDashboardUpdated, EventDashboardDeleted:
		return parts[1], et, true
	}
	return "", "", false
}

// Handle processes one message: its NATS subject and raw payload.
//
// The subject is the authority on which workspace an event belongs to and what kind of event
// it is — that is what a JetStream consumer filters and what a publisher's subject-level
// permissions would constrain. The envelope must agree with it; an event whose payload says
// one workspace while its subject says another is refused rather than trusted either way.
func (p *Processor) Handle(ctx context.Context, subject string, payload []byte) Result {
	workspace, eventType, ok := parseSubject(subject)
	if !ok {
		return Result{Action: Drop, Reason: fmt.Sprintf("subject %q is not a dashboard lifecycle event", subject)}
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

	var data DashboardData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return Result{Action: Drop, Reason: "malformed " + eventType + " data: " + err.Error()}
	}

	var (
		applied bool
		err     error
	)
	if eventType == EventDashboardDeleted {
		applied, err = p.catalog.Remove(ctx, workspace, env.PublishedBy, data.DashboardID, env.PublishedAt)
	} else {
		applied, err = p.catalog.Apply(ctx, dashboards.Upsert{
			Workspace: workspace, SourceModule: env.PublishedBy, ExternalID: data.DashboardID,
			Name: data.Name, Description: data.Description, Owner: data.Owner, Path: data.Path,
			LineageComplete: data.LineageComplete, Sources: data.Sources, At: env.PublishedAt,
		})
	}

	var invalid *asset.ValidationError
	switch {
	case errors.As(err, &invalid):
		// Something about the event itself is unusable: no retry will change that.
		return Result{Action: Drop, Reason: fmt.Sprintf("invalid %s from %q: %v", eventType, env.PublishedBy, invalid)}
	case err != nil:
		return Result{Action: Retry, Reason: err.Error()}
	case !applied:
		return Result{Action: Ack, Reason: "stale: an event for this dashboard published later was already applied"}
	}
	return Result{Action: Ack, Applied: true}
}
