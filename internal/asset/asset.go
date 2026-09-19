// Package asset is the small shared kernel under booth-catalog's three asset-type
// packages (internal/data, internal/code, internal/dashboards): the error vocabulary, the
// page and search-hit shapes, and the text-validation and text-matching helpers they all use.
//
// It exists so those three packages never import each other. Per the brief and ADR 0001,
// the data, code and dashboard catalogs are one repo today but must stay separable along
// folder boundaries — anything more than these few primitives shared between them would be
// a coupling a future split has to fight. If you find yourself adding asset-specific logic
// here, it belongs in one of the three packages instead.
package asset

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Type names one of the three cataloged asset types (ADR 0001, ADR 0018). The set is
// deliberately fixed: whether it generalizes to pipelines or notebooks is an open
// architectural question (ARCHITECTURE.md §7 item 12), not one to answer by adding a value.
type Type string

const (
	TypeData      Type = "data"
	TypeCode      Type = "code"
	TypeDashboard Type = "dashboard"
)

// Types lists every asset type, in the order the UI presents them.
var Types = []Type{TypeData, TypeCode, TypeDashboard}

// ParseTypes parses a comma-separated type filter such as "data,code". An empty string
// means "all types".
func ParseTypes(csv string) ([]Type, error) {
	if strings.TrimSpace(csv) == "" {
		return Types, nil
	}
	var out []Type
	seen := map[Type]bool{}
	for _, part := range strings.Split(csv, ",") {
		t := Type(strings.TrimSpace(part))
		switch t {
		case TypeData, TypeCode, TypeDashboard:
		default:
			return nil, Invalid("types", "unknown asset type %q (want data, code or dashboard)", t)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

var (
	// ErrNotFound means the asset doesn't exist in the caller's workspace. Workspaces are
	// hard tenancy boundaries, so an asset in another workspace is indistinguishable from
	// one that doesn't exist.
	ErrNotFound = errors.New("not found")
	// ErrExists means a uniqueness rule was violated (a name already taken in the workspace,
	// a code version label already published).
	ErrExists = errors.New("already exists")
)

// ValidationError is a request the caller can fix. Field names the offending JSON field
// so a form can put the message next to the input.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Invalid builds a ValidationError.
func Invalid(field, format string, args ...any) *ValidationError {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Page is one page of a listing plus the total match count, so a UI can say "51–100 of 312".
type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

const (
	// DefaultLimit is the page size when a caller doesn't ask for one.
	DefaultLimit = 50
	// MaxLimit caps a page. A catalog is browsed, not exported.
	MaxLimit = 200
)

// ClampLimit applies DefaultLimit/MaxLimit to a caller-supplied page size.
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// Hit is one cross-asset search result (ADR 0044). It carries just enough to render a row
// and link to the asset's own detail view — search never returns a full asset.
type Hit struct {
	Type        Type   `json:"type"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	// Source is the module that published a dashboard ("superset", "metabase",
	// "streamlit"); empty for datasets and code.
	Source string `json:"source,omitempty"`
	// NameMatch records whether the query matched the name (as opposed to only the
	// description or tags). Search has no ranking model (ADR 0044), but listing name matches
	// first is a free, obvious improvement. Not serialized: it's an ordering input.
	NameMatch bool `json:"-"`
}

// SortHits orders hits name-matches first, then by name, then by ID for determinism.
func SortHits(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.NameMatch != b.NameMatch {
			return a.NameMatch
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.ID < b.ID
	})
}

// Actor is the caller on whose behalf a write happens, recorded for provenance.
type Actor struct {
	// Subject is the token's `sub` claim — stable and unique.
	Subject string
	// DisplayName is the human-readable form (preferred_username, email, else subject),
	// used to default an owner the caller didn't name.
	DisplayName string
}

// ---- storage locations (ADR 0045) --------------------------------------------

// Location references a place in booth-storage as the platform-wide {backendId, path} pair
// (ADR 0045): no URI scheme, no embedded credentials. It is only ever stored and compared
// here — resolving it means calling booth-storage's own API. Nothing in this repo opens a
// connection to a backend from a stored Location, which would bypass booth-storage's
// ownership of credentials (ADR 0039).
type Location struct {
	BackendID string `json:"backendId"`
	// Path is "/"-separated and relative to the backend root, with no leading or trailing
	// "/". The empty string is the backend root.
	Path string `json:"path"`
}

// backendIDRE is booth-storage's backend-ID grammar (its registry.ValidateID). Mirrored, not
// imported: this repo takes no code dependency on booth-storage, only on its API shape.
var backendIDRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// maxPathLen matches booth-storage's own path bound.
const maxPathLen = 1024

// NormalizeLocation validates l against booth-storage's path rules (ADR 0037's
// normalization: relative, "/"-separated, no empty/"."/".." segments) and returns it in
// canonical form: a single trailing "/" is dropped, so "warehouse/orders/" and
// "warehouse/orders" name the same folder. field prefixes any error ("location" →
// "location.path").
func NormalizeLocation(field string, l Location) (Location, error) {
	if !backendIDRE.MatchString(l.BackendID) {
		return Location{}, Invalid(field+".backendId", "must be a booth-storage backend ID: 1-63 lowercase letters, digits and hyphens, starting and ending with a letter or digit")
	}
	p := strings.TrimSuffix(l.Path, "/")
	if len(p) > maxPathLen {
		return Location{}, Invalid(field+".path", "must be at most %d bytes", maxPathLen)
	}
	if strings.ContainsAny(p, "\x00\\") {
		return Location{}, Invalid(field+".path", "must not contain NUL or backslash")
	}
	if p != "" {
		for _, seg := range strings.Split(p, "/") {
			switch seg {
			case "":
				return Location{}, Invalid(field+".path", "must not have empty segments or a leading '/'")
			case ".", "..":
				return Location{}, Invalid(field+".path", "must not contain %q segments", seg)
			}
		}
	}
	return Location{BackendID: l.BackendID, Path: p}, nil
}

// PathContains reports whether p is container itself or lies beneath it, on a "/" boundary
// ("warehouse/orders" contains "warehouse/orders/2026/part-1.parquet" but not
// "warehouse/orders-archive"). Both must already be normalized. An empty container is the
// backend root and contains everything. This is the rule that ties a lineage source
// (a location a dashboard reads) to a dataset (a location someone registered).
func PathContains(container, p string) bool {
	return container == "" || p == container || strings.HasPrefix(p, container+"/")
}

// ---- text matching -----------------------------------------------------------

// Terms splits a search query into its lowercased whitespace-separated terms. A record
// matches when every term appears somewhere in its searchable text (ADR 0044: substring
// match, no ranking, no faceting).
func Terms(q string) []string {
	return strings.Fields(strings.ToLower(q))
}

// MatchesAll reports whether every term occurs (case-insensitively) in at least one of
// fields. The in-memory stores use this; the Postgres stores express the same rule as
// ILIKE, and each store's contract tests run identical cases so the two can't drift.
func MatchesAll(terms []string, fields ...string) bool {
	if len(terms) == 0 {
		return true
	}
	lowered := make([]string, len(fields))
	for i, f := range fields {
		lowered[i] = strings.ToLower(f)
	}
	for _, term := range terms {
		found := false
		for _, f := range lowered {
			if strings.Contains(f, term) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// LikePattern turns a search term into a Postgres ILIKE pattern that matches it as a
// literal substring: `\`, `%` and `_` in the term are escaped (Postgres's default LIKE
// escape character is the backslash), so a user searching for "100%" or "a_b" is not
// accidentally running a wildcard query.
func LikePattern(term string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range term {
		if r == '\\' || r == '%' || r == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// ---- validation helpers ------------------------------------------------------

// NewID returns a fresh asset ID. Random UUIDs, not names: a renamed asset keeps its ID, so
// references to it (a dashboard's lineage edge, a future pipeline's code reference) survive.
func NewID() string { return uuid.NewString() }

var workspaceRE = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

// ValidWorkspace reports whether ws has ADR 0025's slug shape. Every store call is
// workspace-scoped; this guards the seam where a workspace arrives from outside (an event
// subject, say) rather than from an already-verified token.
func ValidWorkspace(ws string) bool { return workspaceRE.MatchString(ws) }

// Text validates and normalizes a free-text field: it is trimmed, must be valid UTF-8
// containing no control characters (newline and tab are allowed when multiline), and is
// bounded to max characters. An empty result is an error when required.
func Text(field, s string, max int, required, multiline bool) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", Invalid(field, "must be valid UTF-8 text")
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t' || r == '\r')) {
			return "", Invalid(field, "must not contain control characters")
		}
	}
	if n := utf8.RuneCountInString(s); n > max {
		return "", Invalid(field, "must be at most %d characters (got %d)", max, n)
	}
	if required && s == "" {
		return "", Invalid(field, "is required")
	}
	return s, nil
}
