// Package dashboards is the dashboard half of booth-catalog: an index of the dashboards
// (and, for booth-streamlit, apps) that booth-superset, booth-metabase and booth-streamlit
// publish, each with an owner and lineage back to the datasets it reads (ADR 0018).
//
// Dashboards are never created here. This package holds no create/edit API and no UI for
// building one; a dashboard exists in the catalog only because a dashboard module told the
// event bus about it, and disappears when the module says it is gone. The transport side of
// that — NATS subjects, the envelope, acking — lives in internal/events; this package sees
// only already-parsed Upserts, so it is testable with no broker at all.
//
// The event payload this package implements is a proposal awaiting coordinator sign-off:
// see docs/decisions/0001-dashboard-event-payload.md.
package dashboards

import (
	"regexp"
	"strings"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

const (
	maxExternalID  = 200
	maxName        = 200
	maxDescription = 4000
	maxOwner       = 200
	maxPath        = 500
	maxSourceText  = 300
	maxSources     = 200
)

// moduleRE is a module manifest ID ("superset", "metabase", "streamlit"): what the event
// envelope carries as publishedBy.
var moduleRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// SourceType says how a dashboard names something it reads.
type SourceType string

const (
	// SourceDataset names a catalog dataset directly, by its catalog ID. Precise, but only
	// available to a publisher that got the dataset from the catalog.
	SourceDataset SourceType = "dataset"
	// SourceLocation names a booth-storage location, {backendId, path} (ADR 0045). The
	// catalog resolves it to whichever registered dataset(s) contain that location. This is
	// the natural form for an app that reads files (a Streamlit app pulling Parquet).
	SourceLocation SourceType = "location"
	// SourceExternal names something the catalog has no way to resolve — a table in a
	// database the dashboard tool connects to itself. It is shown, honestly, as an
	// unresolved source; it is never guessed at.
	SourceExternal SourceType = "external"
)

// Source is one thing a dashboard reads. Which fields are meaningful depends on Type.
type Source struct {
	Type      SourceType `json:"type"`
	DatasetID string     `json:"datasetId,omitempty"`
	BackendID string     `json:"backendId,omitempty"`
	Path      string     `json:"path,omitempty"`
	System    string     `json:"system,omitempty"`
	Name      string     `json:"name,omitempty"`
}

// Normalize validates a source and returns it in canonical form, with every field that
// doesn't belong to its Type cleared (so two publishers describing the same source in
// slightly different ways produce identical stored rows).
func (s Source) Normalize() (Source, error) {
	switch s.Type {
	case SourceDataset:
		id, err := asset.Text("datasetId", s.DatasetID, 64, true, false)
		if err != nil {
			return Source{}, err
		}
		return Source{Type: SourceDataset, DatasetID: id}, nil
	case SourceLocation:
		loc, err := asset.NormalizeLocation("location", asset.Location{BackendID: s.BackendID, Path: s.Path})
		if err != nil {
			return Source{}, err
		}
		return Source{Type: SourceLocation, BackendID: loc.BackendID, Path: loc.Path}, nil
	case SourceExternal:
		name, err := asset.Text("name", s.Name, maxSourceText, true, false)
		if err != nil {
			return Source{}, err
		}
		system, err := asset.Text("system", s.System, maxSourceText, false, false)
		if err != nil {
			return Source{}, err
		}
		return Source{Type: SourceExternal, Name: name, System: system}, nil
	}
	return Source{}, asset.Invalid("type", "must be %q, %q or %q", SourceDataset, SourceLocation, SourceExternal)
}

// Dashboard is an indexed dashboard.
type Dashboard struct {
	ID        string `json:"id"`
	Workspace string `json:"-"`
	// SourceModule is the publishing module's manifest ID ("superset", ...), the event
	// envelope's publishedBy. With ExternalID it is the dashboard's identity.
	SourceModule string `json:"sourceModule"`
	// ExternalID is the dashboard's ID inside the publishing tool.
	ExternalID  string `json:"externalId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Owner is a free-form identifier as the publishing tool reports it — see
	// docs/decisions/0003-owner-identity.md.
	Owner string `json:"owner"`
	// Path is a shell-relative path that opens the dashboard (under the publisher's navPath),
	// or empty if the publisher didn't give one.
	Path string `json:"path"`
	// LineageComplete is the publisher's assertion that Sources lists everything the
	// dashboard reads. False (the default) means "may be incomplete" — which is all a
	// code-first Streamlit app can honestly claim (ADR 0018).
	LineageComplete bool `json:"lineageComplete"`
	// Sources is what the dashboard reads, as the publisher named it. Unresolved: see Lineage
	// for the catalog's reading of it.
	Sources []Source `json:"-"`
	// CreatedAt is when the catalog first indexed it; UpdatedAt is the publishedAt of the
	// latest event applied to it.
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Upsert is one parsed dashboard.created / dashboard.updated event: the dashboard's full
// current state as the publisher sees it.
type Upsert struct {
	Workspace       string
	SourceModule    string
	ExternalID      string
	Name            string
	Description     string
	Owner           string
	Path            string
	LineageComplete bool
	Sources         []Source
	// At is the event's publishedAt. It orders events for one dashboard: an event older than
	// what has already been applied is ignored (delivery is at-least-once and unordered).
	At time.Time

	// NewID and ReceivedAt are filled in by the Service for the store's use when the event
	// creates a dashboard; they are ignored when it updates an existing one.
	NewID      string
	ReceivedAt time.Time
}

// Normalize validates the upsert and returns it in canonical form. It is deliberately
// forgiving about lineage and strict about identity: an individual source that can't be
// understood is dropped (and counted) rather than losing the whole dashboard, and the
// lineage is then marked incomplete since something was left out. Identity, name and time
// are required — without them there is nothing to index.
func (u Upsert) Normalize() (out Upsert, droppedSources int, err error) {
	out = u
	if !asset.ValidWorkspace(u.Workspace) {
		return Upsert{}, 0, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	if !moduleRE.MatchString(u.SourceModule) {
		return Upsert{}, 0, asset.Invalid("publishedBy", "must be a module ID such as \"superset\"")
	}
	if out.ExternalID, err = asset.Text("dashboardId", u.ExternalID, maxExternalID, true, false); err != nil {
		return Upsert{}, 0, err
	}
	if out.Name, err = asset.Text("name", u.Name, maxName, true, false); err != nil {
		return Upsert{}, 0, err
	}
	if out.Description, err = asset.Text("description", u.Description, maxDescription, false, true); err != nil {
		return Upsert{}, 0, err
	}
	if out.Owner, err = asset.Text("owner", u.Owner, maxOwner, false, false); err != nil {
		return Upsert{}, 0, err
	}
	if out.Path, err = normalizePath(u.Path); err != nil {
		return Upsert{}, 0, err
	}
	if u.At.IsZero() {
		return Upsert{}, 0, asset.Invalid("publishedAt", "is required: it orders this event against others for the same dashboard")
	}
	// Postgres keeps microseconds; truncating here means every store compares event times at
	// the same precision, so "is this event stale?" has one answer everywhere.
	out.At = u.At.UTC().Truncate(time.Microsecond)

	out.Sources = make([]Source, 0, len(u.Sources))
	seen := map[Source]bool{}
	for _, raw := range u.Sources {
		s, err := raw.Normalize()
		if err != nil || len(out.Sources) >= maxSources {
			droppedSources++
			continue
		}
		if !seen[s] {
			seen[s] = true
			out.Sources = append(out.Sources, s)
		}
	}
	if droppedSources > 0 {
		out.LineageComplete = false
	}
	return out, droppedSources, nil
}

// normalizePath accepts only a path within the shell — "/superset/dashboard/42", optionally
// with a query. It refuses anything a browser could read as leaving the shell: a scheme, a
// protocol-relative "//host", or a backslash. The UI links to this value, and a dashboard
// module is a third party from the catalog's point of view.
func normalizePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if len(p) > maxPath {
		return "", asset.Invalid("path", "must be at most %d characters", maxPath)
	}
	if p[0] != '/' || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\x00") || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }) {
		return "", asset.Invalid("path", "must be a path within the platform shell, starting with a single \"/\" (for example \"/superset/dashboard/42\")")
	}
	return p, nil
}

// ListFilter narrows a dashboard listing. Every field is optional and they combine with AND.
type ListFilter struct {
	// Query matches names and descriptions (ADR 0044's substring semantics).
	Query string
	// Owner matches case-insensitively and exactly.
	Owner string
	// SourceModule matches exactly ("superset").
	SourceModule string
	Limit        int
	Offset       int
}

// DatasetRef identifies a catalog dataset in lineage output.
type DatasetRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ResolvedSource is a Source together with the catalog datasets it resolves to. An empty
// Datasets means the source did not resolve — always so for an external source, and for a
// dataset or location source whose dataset isn't (or is no longer) registered.
type ResolvedSource struct {
	Source
	Datasets []DatasetRef `json:"datasets"`
}

// Lineage is a dashboard's sources as the catalog currently resolves them. Resolution
// happens on every read, not when the event arrives, so registering a dataset after a
// dashboard was indexed connects the two without any re-processing.
type Lineage struct {
	Complete bool             `json:"complete"`
	Sources  []ResolvedSource `json:"sources"`
}

// Detail is a dashboard with its resolved lineage.
type Detail struct {
	Dashboard
	Lineage Lineage `json:"lineage"`
}
