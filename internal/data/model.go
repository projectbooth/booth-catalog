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
)

// tagRE is the tag grammar: lowercase letters, digits and . _ : - (so "pii", "q3-2026" and
// "team:finance" all work), starting with a letter or digit. Tags are normalized to
// lowercase before this is checked, so "PII" and "pii" are one tag and filtering is exact.
var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*$`)

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

	if len(in.Schema) > maxColumns {
		return Input{}, asset.Invalid("schema", "must have at most %d columns (got %d)", maxColumns, len(in.Schema))
	}
	out.Schema = make([]Column, 0, len(in.Schema))
	seen := make(map[string]bool, len(in.Schema))
	for i, c := range in.Schema {
		field := "schema[" + strconv.Itoa(i) + "]"
		col := Column{}
		if col.Name, err = asset.Text(field+".name", c.Name, maxName, true, false); err != nil {
			return Input{}, err
		}
		if col.Type, err = asset.Text(field+".type", c.Type, maxColumnType, true, false); err != nil {
			return Input{}, err
		}
		if col.Description, err = asset.Text(field+".description", c.Description, maxColumnDesc, false, false); err != nil {
			return Input{}, err
		}
		if seen[col.Name] {
			return Input{}, asset.Invalid(field+".name", "duplicate column name %q", col.Name)
		}
		seen[col.Name] = true
		out.Schema = append(out.Schema, col)
	}

	if out.Tags, err = normalizeTags(in.Tags); err != nil {
		return Input{}, err
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
