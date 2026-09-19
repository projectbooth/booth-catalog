package data

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

func validInput() Input {
	return Input{
		Name:     "orders",
		Location: asset.Location{BackendID: "lake", Path: "warehouse/orders"},
	}
}

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var ve *asset.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a *asset.ValidationError", err)
	}
	return ve.Field
}

func TestNormalize_CanonicalForm(t *testing.T) {
	got, err := Input{
		Name:        "  orders  ",
		Description: "  one row per order\nsecond line ",
		Owner:       " alice ",
		Location:    asset.Location{BackendID: "lake", Path: "warehouse/orders/"},
		Schema:      []Column{{Name: " id ", Type: " bigint ", Description: " primary key "}},
		Tags:        []string{"PII", " finance ", "pii", "q3-2026", "team:data.eng"},
	}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	want := Input{
		Name:        "orders",
		Description: "one row per order\nsecond line",
		Owner:       "alice",
		Location:    asset.Location{BackendID: "lake", Path: "warehouse/orders"}, // trailing slash dropped
		Schema:      []Column{{Name: "id", Type: "bigint", Description: "primary key"}},
		Tags:        []string{"finance", "pii", "q3-2026", "team:data.eng"}, // lowercased, deduped, sorted
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Normalize:\n got %+v\nwant %+v", got, want)
	}
}

// JSON must carry [] rather than null for an omitted schema or tag list, so a client never
// has to null-check a collection.
func TestNormalize_EmptyCollectionsAreNotNil(t *testing.T) {
	got, err := validInput().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema == nil || got.Tags == nil {
		t.Errorf("Schema=%v Tags=%v, want non-nil empty slices", got.Schema, got.Tags)
	}
}

func TestNormalize_Rejects(t *testing.T) {
	long := strings.Repeat("x", 201)
	manyCols := make([]Column, maxColumns+1) // the count is checked before per-column rules
	manyTags := make([]string, maxTags+1)
	for i := range manyTags {
		manyTags[i] = "t" + strings.Repeat("a", i)
	}

	cases := []struct {
		name  string
		mod   func(*Input)
		field string
	}{
		{"missing name", func(i *Input) { i.Name = "   " }, "name"},
		{"name too long", func(i *Input) { i.Name = long }, "name"},
		{"control character in name", func(i *Input) { i.Name = "a\x07b" }, "name"},
		{"newline in name", func(i *Input) { i.Name = "a\nb" }, "name"},
		{"description too long", func(i *Input) { i.Description = strings.Repeat("d", maxDescription+1) }, "description"},
		{"owner too long", func(i *Input) { i.Owner = long }, "owner"},
		{"missing backend", func(i *Input) { i.Location.BackendID = "" }, "location.backendId"},
		{"backend id with uppercase", func(i *Input) { i.Location.BackendID = "Lake" }, "location.backendId"},
		{"backend id with a slash", func(i *Input) { i.Location.BackendID = "a/b" }, "location.backendId"},
		{"absolute path", func(i *Input) { i.Location.Path = "/warehouse" }, "location.path"},
		{"parent traversal", func(i *Input) { i.Location.Path = "a/../b" }, "location.path"},
		{"dot segment", func(i *Input) { i.Location.Path = "a/./b" }, "location.path"},
		{"empty segment", func(i *Input) { i.Location.Path = "a//b" }, "location.path"},
		{"backslash", func(i *Input) { i.Location.Path = `a\b` }, "location.path"},
		{"NUL byte", func(i *Input) { i.Location.Path = "a\x00b" }, "location.path"},
		{"path too long", func(i *Input) { i.Location.Path = strings.Repeat("a", 1025) }, "location.path"},
		{"too many columns", func(i *Input) { i.Schema = manyCols }, "schema"},
		{"column without a name", func(i *Input) { i.Schema = []Column{{Type: "int"}} }, "schema[0].name"},
		{"column without a type", func(i *Input) { i.Schema = []Column{{Name: "id"}} }, "schema[0].type"},
		{"duplicate column", func(i *Input) { i.Schema = []Column{{Name: "id", Type: "int"}, {Name: "id", Type: "text"}} }, "schema[1].name"},
		{"too many tags", func(i *Input) { i.Tags = manyTags }, "tags"},
		{"tag with a space", func(i *Input) { i.Tags = []string{"two words"} }, "tags"},
		{"tag starting with a dash", func(i *Input) { i.Tags = []string{"-x"} }, "tags"},
		{"empty tag", func(i *Input) { i.Tags = []string{" "} }, "tags"},
		{"tag too long", func(i *Input) { i.Tags = []string{strings.Repeat("a", maxTagLen+1)} }, "tags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mod(&in)
			_, err := in.Normalize()
			if err == nil {
				t.Fatal("Normalize accepted invalid input")
			}
			if got := fieldOf(t, err); got != tc.field {
				t.Errorf("field = %q, want %q (%v)", got, tc.field, err)
			}
		})
	}
}

// The backend root is a legitimate location: a whole backend can be one dataset.
func TestNormalize_AcceptsTheBackendRoot(t *testing.T) {
	for _, p := range []string{"", "/"} {
		in := validInput()
		in.Location.Path = p
		got, err := in.Normalize()
		if err != nil {
			t.Fatalf("path %q rejected: %v", p, err)
		}
		if got.Location.Path != "" {
			t.Errorf("path %q normalized to %q, want the empty root", p, got.Location.Path)
		}
	}
}
