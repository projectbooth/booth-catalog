package code

import (
	"errors"
	"strings"
	"testing"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var ve *asset.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a *asset.ValidationError", err)
	}
	return ve.Field
}

func TestEntryInput_Normalize(t *testing.T) {
	got, err := EntryInput{Name: "  clean_emails ", Description: " strips PII ", Owner: " bob ", Language: " Python "}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got != (EntryInput{Name: "clean_emails", Description: "strips PII", Owner: "bob", Language: "python"}) {
		t.Errorf("Normalize = %+v", got)
	}

	for _, tc := range []struct {
		name  string
		in    EntryInput
		field string
	}{
		{"missing name", EntryInput{}, "name"},
		{"name too long", EntryInput{Name: strings.Repeat("n", 201)}, "name"},
		{"description too long", EntryInput{Name: "x", Description: strings.Repeat("d", 4001)}, "description"},
		{"owner too long", EntryInput{Name: "x", Owner: strings.Repeat("o", 201)}, "owner"},
		{"language with a space", EntryInput{Name: "x", Language: "visual basic"}, "language"},
		{"language too long", EntryInput{Name: "x", Language: strings.Repeat("l", 33)}, "language"},
	} {
		if _, err := tc.in.Normalize(); err == nil || fieldOf(t, err) != tc.field {
			t.Errorf("%s: err = %v, want a %q validation error", tc.name, err, tc.field)
		}
	}
	// Optional fields may be blank; c++ and c# are legitimate labels.
	for _, lang := range []string{"", "c++", "c#", "sql", "objective-c"} {
		if _, err := (EntryInput{Name: "x", Language: lang}).Normalize(); err != nil {
			t.Errorf("language %q rejected: %v", lang, err)
		}
	}
}

func TestVersionInput_Normalize(t *testing.T) {
	// The source is preserved byte for byte: consumers may hash or diff it.
	src := "\n  def f():\r\n    return 1  \n\n"
	got, err := VersionInput{Version: " 1.0.0 ", Source: src, Notes: " first "}.Normalize(1024)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.0.0" || got.Notes != "first" || got.Source != src {
		t.Errorf("Normalize = %+v (source must be untouched)", got)
	}

	for _, label := range []string{"1", "1.0.0", "v2", "2026-09-19", "1.2.3-rc.1+build.5", "A_b", strings.Repeat("v", 64)} {
		if _, err := (VersionInput{Version: label, Source: "x"}).Normalize(10); err != nil {
			t.Errorf("label %q rejected: %v", label, err)
		}
	}

	for _, tc := range []struct {
		name  string
		in    VersionInput
		field string
	}{
		{"no label", VersionInput{Source: "x"}, "version"},
		{"label with a slash breaks the URL", VersionInput{Version: "1/2", Source: "x"}, "version"},
		{"label with a space", VersionInput{Version: "1 0", Source: "x"}, "version"},
		{"label starting with a dot", VersionInput{Version: ".1", Source: "x"}, "version"},
		{"label too long", VersionInput{Version: strings.Repeat("v", 65), Source: "x"}, "version"},
		{"latest is reserved", VersionInput{Version: "latest", Source: "x"}, "version"},
		{"latest is reserved after trimming", VersionInput{Version: " latest ", Source: "x"}, "version"},
		{"blank source", VersionInput{Version: "1", Source: " \n\t "}, "source"},
		{"source over the limit", VersionInput{Version: "1", Source: strings.Repeat("x", 11)}, "source"},
		{"NUL in source", VersionInput{Version: "1", Source: "a\x00b"}, "source"},
		{"invalid UTF-8", VersionInput{Version: "1", Source: "a\xffb"}, "source"},
		{"notes too long", VersionInput{Version: "1", Source: "x", Notes: strings.Repeat("n", 4001)}, "notes"},
	} {
		if _, err := tc.in.Normalize(10); err == nil || fieldOf(t, err) != tc.field {
			t.Errorf("%s: err = %v, want a %q validation error", tc.name, err, tc.field)
		}
	}
	// The limit is in bytes: 6 two-byte characters is 12 bytes, over a 10-byte limit.
	if _, err := (VersionInput{Version: "1", Source: strings.Repeat("é", 6)}).Normalize(10); err == nil {
		t.Error("multi-byte source over the byte limit was accepted")
	}
}
