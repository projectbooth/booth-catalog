// Package data is the dataset half of booth-catalog: register and browse datasets with a
// name, description, storage location, schema, tags and an owner (ADR 0042).
//
// A dataset's location is a booth-storage {backendId, path} pair (ADR 0045), stored as-is
// and never resolved here — resolution is a call to booth-storage's own API, made by the
// caller (the UI, through the gateway, with the user's own token). This package therefore
// has no dependency on booth-storage at all, and a catalog entry keeps working, and stays
// browsable, when the location it points at has since moved or vanished.
package data

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

const (
	maxName        = 200
	maxDescription = 4000
	maxOwner       = 200
	maxColumns     = 1000
	maxColumnType  = 100
	maxColumnDesc  = 1000
	maxTags        = 20
	maxTagLen      = 50
	maxNamespace   = 200
	maxTableName   = 200
	maxUUID        = 64
)

// tagRE is the tag grammar: lowercase letters, digits and . _ : - (so "pii", "q3-2026" and
// "team:finance" all work), starting with a letter or digit. Tags are normalized to
// lowercase before this is checked, so "PII" and "pii" are one tag and filtering is exact.
var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*$`)

// moduleRE is a module manifest ID ("lakehouse"): what the event envelope carries as
// publishedBy for a table.* event. Mirrors internal/dashboards' identical grammar.
var moduleRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Format discriminates what a Dataset row actually is (ADR 0085). FormatFile is every
// dataset registered before this existed and everything registered by hand through this
// package's own write API; FormatIceberg is a table booth-lakehouse published.
type Format string

const (
	FormatFile    Format = "file"
	FormatIceberg Format = "iceberg"
)

// TableRef is the Iceberg-specific identity a format: "iceberg" row carries (ADR 0085),
// populated from booth-lakehouse's table.* events and never set by hand. Search (ADR 0044),
// lineage (ADR 0046) and permissions (ADR 0048) don't look at it at all — to anyone browsing
// or querying lineage this is a Dataset like any other.
type TableRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	// CurrentSnapshotID is nil for a table with no snapshot yet (created but never committed
	// to). booth-lakehouse's full snapshot history lives in booth-lakehouse, not here.
	CurrentSnapshotID *int64 `json:"currentSnapshotId,omitempty"`
}

// Column is one field of a dataset's schema. Type is free-form ("string", "bigint",
// "timestamp(6)"): the catalog describes what a dataset holds in whatever vocabulary its
// producer uses, and deliberately doesn't try to normalize types across formats — the
// platform hasn't standardized on a table format (ARCHITECTURE.md §7 item 21).
type Column struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// Dataset is a registered dataset (ADR 0042).
type Dataset struct {
	ID          string         `json:"id"`
	Workspace   string         `json:"-"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Location    asset.Location `json:"location"`
	Schema      []Column       `json:"schema"`
	Tags        []string       `json:"tags"`
	// Owner is a free-form identifier, defaulting to the registering user's
	// preferred_username/email — see docs/decisions/0003-owner-identity.md.
	Owner string `json:"owner"`
	// CreatedBy is the registering user's token subject, recorded for provenance. It is
	// distinct from Owner: ownership can be assigned to someone else, or to a team.
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Format discriminates a plain registered dataset from an Iceberg table (ADR 0085).
	// Defaults to FormatFile, so every dataset registered before this field existed reads
	// exactly as it always did.
	Format Format `json:"format"`
	// Table is populated only when Format is FormatIceberg, set from booth-lakehouse's
	// table.* events (SourceModule below) and never editable through this package's own
	// write API — see Service.Update/Delete.
	Table *TableRef `json:"table,omitempty"`
	// SourceModule is the publishing module's manifest ID (e.g. "lakehouse") for a
	// format: "iceberg" row — the event envelope's publishedBy. Empty for a FormatFile row.
	// With Table.UUID it is the row's identity for applying table.* events, mirroring how
	// internal/dashboards identifies a dashboard by (SourceModule, ExternalID).
	SourceModule string `json:"-"`
}

// Input is what a caller supplies to register or update a dataset.
type Input struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Location    asset.Location `json:"location"`
	Schema      []Column       `json:"schema"`
	Tags        []string       `json:"tags"`
	Owner       string         `json:"owner"`
}

// Normalize validates the input and returns it in canonical form: text trimmed, the
// location normalized, tags lowercased/deduplicated/sorted, and nil slices made empty (so
// JSON always carries [] rather than null). Owner may be left empty; the service fills it.
func (in Input) Normalize() (Input, error) {
	var err error
	out := Input{}

	if out.Name, err = asset.Text("name", in.Name, maxName, true, false); err != nil {
		return Input{}, err
	}
	if out.Description, err = asset.Text("description", in.Description, maxDescription, false, true); err != nil {
		return Input{}, err
	}
	if out.Owner, err = asset.Text("owner", in.Owner, maxOwner, false, false); err != nil {
		return Input{}, err
	}
	if out.Location, err = asset.NormalizeLocation("location", in.Location); err != nil {
		return Input{}, err
	}

	if out.Schema, err = normalizeSchema(in.Schema); err != nil {
		return Input{}, err
	}

	if out.Tags, err = normalizeTags(in.Tags); err != nil {
		return Input{}, err
	}
	return out, nil
}

// normalizeSchema validates a set of columns, shared by Input.Normalize (a hand-typed schema)
// and TableUpsert.Normalize (an Iceberg table's current schema, mapped from the event).
func normalizeSchema(in []Column) ([]Column, error) {
	if len(in) > maxColumns {
		return nil, asset.Invalid("schema", "must have at most %d columns (got %d)", maxColumns, len(in))
	}
	out := make([]Column, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, c := range in {
		field := "schema[" + strconv.Itoa(i) + "]"
		col := Column{}
		var err error
		if col.Name, err = asset.Text(field+".name", c.Name, maxName, true, false); err != nil {
			return nil, err
		}
		if col.Type, err = asset.Text(field+".type", c.Type, maxColumnType, true, false); err != nil {
			return nil, err
		}
		if col.Description, err = asset.Text(field+".description", c.Description, maxColumnDesc, false, false); err != nil {
			return nil, err
		}
		if seen[col.Name] {
			return nil, asset.Invalid(field+".name", "duplicate column name %q", col.Name)
		}
		seen[col.Name] = true
		out = append(out, col)
	}
	return out, nil
}

func normalizeTags(in []string) ([]string, error) {
	if len(in) > maxTags {
		return nil, asset.Invalid("tags", "must have at most %d tags (got %d)", maxTags, len(in))
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" || len(t) > maxTagLen || !tagRE.MatchString(t) {
			return nil, asset.Invalid("tags", "tag %q is invalid: use 1-%d lowercase letters, digits and . _ : - characters, starting with a letter or digit", raw, maxTagLen)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

// TableUpsert is one parsed table.created / table.updated event (ADR 0085): an Iceberg
// table's current state as booth-lakehouse sees it, minus its full snapshot history.
type TableUpsert struct {
	Workspace         string
	SourceModule      string
	Namespace         string
	Name              string
	UUID              string
	Location          asset.Location
	Schema            []Column
	CurrentSnapshotID *int64
	// At is the event's publishedAt: it orders events for one table exactly like
	// internal/dashboards.Upsert.At, so a redelivered or out-of-order event can never roll a
	// table's catalog row back.
	At time.Time

	// NewID and ReceivedAt are filled in by the Service for the store's use when the event
	// creates a row; ignored when it updates an existing one.
	NewID      string
	ReceivedAt time.Time
}

// Normalize validates the upsert and returns it in canonical form. Identity (workspace,
// publishing module, table UUID) and a name are required; everything else defaults sensibly,
// since booth-lakehouse's own metadata is the source of truth this is only mirroring.
func (u TableUpsert) Normalize() (TableUpsert, error) {
	out := u
	var err error
	if !asset.ValidWorkspace(u.Workspace) {
		return TableUpsert{}, asset.Invalid("workspace", "is not a valid workspace slug")
	}
	if !moduleRE.MatchString(u.SourceModule) {
		return TableUpsert{}, asset.Invalid("publishedBy", "must be a module ID such as \"lakehouse\"")
	}
	if out.Namespace, err = asset.Text("namespace", u.Namespace, maxNamespace, true, false); err != nil {
		return TableUpsert{}, err
	}
	if out.Name, err = asset.Text("name", u.Name, maxTableName, true, false); err != nil {
		return TableUpsert{}, err
	}
	if out.UUID, err = asset.Text("tableUuid", u.UUID, maxUUID, true, false); err != nil {
		return TableUpsert{}, err
	}
	if out.Location, err = asset.NormalizeLocation("location", u.Location); err != nil {
		return TableUpsert{}, err
	}
	if out.Schema, err = normalizeSchema(u.Schema); err != nil {
		return TableUpsert{}, err
	}
	if u.At.IsZero() {
		return TableUpsert{}, asset.Invalid("publishedAt", "is required: it orders this event against others for the same table")
	}
	// Postgres keeps microseconds; truncating here means every store compares event times at
	// the same precision, matching internal/dashboards.Upsert.Normalize.
	out.At = u.At.UTC().Truncate(time.Microsecond)
	return out, nil
}

// datasetName is the catalog dataset name an Iceberg table gets: namespace and table name
// are each names within booth-lakehouse's own catalog, so joining them is what keeps two
// tables in different namespaces from colliding here, the same way they don't collide there.
func (u TableUpsert) datasetName() string { return u.Namespace + "." + u.Name }

// ListFilter narrows a dataset listing. Every field is optional and they combine with AND.
type ListFilter struct {
	// Query matches names, descriptions and tags (ADR 0044's substring semantics).
	Query string
	// Tags must all be present on a dataset for it to match.
	Tags []string
	// Owner matches case-insensitively and exactly.
	Owner  string
	Limit  int
	Offset int
}

// TagCount is a tag and how many datasets carry it, for the UI's tag filter.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}
