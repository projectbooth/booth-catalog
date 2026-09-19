// Package code is the code half of booth-catalog: a registry where a user publishes and
// browses reusable code and functions, with basic metadata (name, description, owner) and a
// version history — several versions per entry, each browsable on its own (ADR 0043).
//
// It is deliberately only a registry. There is no execution, no packaging, no dependency
// resolution, and no notion of an entry being "runnable": whether and how another module
// (booth-pipeline first, ADR 0010) references cataloged code to run it is that module's
// narrow contract to settle, not something to anticipate here. What this package does
// guarantee — because a consumer pinning a version needs it — is that a published version
// is immutable: its source never changes after publication.
package code

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

const (
	maxName        = 200
	maxDescription = 4000
	maxOwner       = 200
	maxNotes       = 4000
	maxLanguage    = 32

	// DefaultMaxSourceBytes bounds one version's source. This is a registry of functions and
	// snippets, not a place to keep vendored libraries or data files.
	DefaultMaxSourceBytes = 1 << 20

	// LatestVersion is the alias the API accepts in place of a version label to mean "the
	// most recently published version". It is reserved: no version may be named this.
	LatestVersion = "latest"
)

// versionRE bounds a version label to something safe in a URL path segment. It is
// deliberately not "must be semver": teams label versions "2026-09-19" or "v3-hotfix", and
// ordering comes from publication order (Seq), not from parsing the label.
var versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// languageRE is a lowercase language label such as "python", "sql" or "c++".
var languageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+#._-]*$`)

// VersionSummary describes one published version without its source, for history listings.
type VersionSummary struct {
	Version string `json:"version"`
	// Seq is the version's publication order within its entry: 1 for the first published,
	// then 2, and so on. "Latest" means highest Seq — the most recently published, which is
	// not necessarily the highest-numbered label.
	Seq       int    `json:"seq"`
	Notes     string `json:"notes"`
	SizeBytes int    `json:"sizeBytes"`
	// PublishedBy is the publisher's human-readable identity (preferred_username or email).
	PublishedBy string    `json:"publishedBy"`
	PublishedAt time.Time `json:"publishedAt"`
}

// Version is one published version including its source.
type Version struct {
	VersionSummary
	Source string `json:"source"`
}

// Entry is a code catalog entry: the stable identity that versions hang off.
type Entry struct {
	ID          string `json:"id"`
	Workspace   string `json:"-"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Owner is a free-form identifier, defaulting to the publishing user — see
	// docs/decisions/0003-owner-identity.md.
	Owner string `json:"owner"`
	// Language is an optional label ("python", "sql") a UI can use for display; the catalog
	// attaches no behavior to it.
	Language  string    `json:"language"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt moves on a metadata edit and on every new version.
	UpdatedAt time.Time `json:"updatedAt"`

	// LatestVersion and VersionCount are derived from the entry's versions, filled in by the
	// store on reads. An entry always has at least one version: creating one requires it.
	LatestVersion *VersionSummary `json:"latestVersion"`
	VersionCount  int             `json:"versionCount"`
}

// EntryInput is the editable metadata of an entry.
type EntryInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	Language    string `json:"language"`
}

// VersionInput is one version to publish.
type VersionInput struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Notes   string `json:"notes"`
}

// CreateInput registers a new entry together with its first version — an entry with no
// versions would have nothing to browse.
type CreateInput struct {
	EntryInput
	VersionInput
}

// Normalize validates entry metadata and returns it in canonical form. Owner may be empty;
// the service fills it.
func (in EntryInput) Normalize() (EntryInput, error) {
	var err error
	out := EntryInput{}
	if out.Name, err = asset.Text("name", in.Name, maxName, true, false); err != nil {
		return EntryInput{}, err
	}
	if out.Description, err = asset.Text("description", in.Description, maxDescription, false, true); err != nil {
		return EntryInput{}, err
	}
	if out.Owner, err = asset.Text("owner", in.Owner, maxOwner, false, false); err != nil {
		return EntryInput{}, err
	}
	lang := strings.ToLower(strings.TrimSpace(in.Language))
	if lang != "" && (len(lang) > maxLanguage || !languageRE.MatchString(lang)) {
		return EntryInput{}, asset.Invalid("language", "must be a short lowercase label such as \"python\" or \"sql\" (letters, digits and + # . _ -, at most %d characters)", maxLanguage)
	}
	out.Language = lang
	return out, nil
}

// Normalize validates a version and returns it in canonical form. maxSource bounds the
// source in bytes.
//
// The source is stored exactly as given — no trimming, no line-ending rewriting — because
// consumers may hash it or diff it. It must be non-blank text: valid UTF-8 with no NUL bytes
// (which Postgres text columns cannot hold, and which mean it isn't source code anyway).
func (in VersionInput) Normalize(maxSource int) (VersionInput, error) {
	var err error
	out := VersionInput{Version: strings.TrimSpace(in.Version), Source: in.Source}
	if out.Version == LatestVersion {
		return VersionInput{}, asset.Invalid("version", "%q is reserved as an alias for the newest version; choose another label", LatestVersion)
	}
	if !versionRE.MatchString(out.Version) {
		return VersionInput{}, asset.Invalid("version", "must be 1-64 characters: letters, digits and . _ + -, starting with a letter or digit (for example \"1.0.0\" or \"2026-09-19\")")
	}
	if out.Notes, err = asset.Text("notes", in.Notes, maxNotes, false, true); err != nil {
		return VersionInput{}, err
	}
	switch {
	case strings.TrimSpace(in.Source) == "":
		return VersionInput{}, asset.Invalid("source", "is required")
	case !utf8.ValidString(in.Source):
		return VersionInput{}, asset.Invalid("source", "must be valid UTF-8 text")
	case strings.ContainsRune(in.Source, 0):
		return VersionInput{}, asset.Invalid("source", "must not contain NUL bytes")
	case len(in.Source) > maxSource:
		return VersionInput{}, asset.Invalid("source", "is %d bytes; a single version is limited to %d", len(in.Source), maxSource)
	}
	return out, nil
}

// ListFilter narrows an entry listing. Every field is optional and they combine with AND.
type ListFilter struct {
	// Query matches names and descriptions (ADR 0044's substring semantics).
	Query string
	// Owner matches case-insensitively and exactly.
	Owner string
	// Language matches exactly (already lowercase).
	Language string
	Limit    int
	Offset   int
}
