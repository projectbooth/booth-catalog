package asset

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseTypes(t *testing.T) {
	all, err := ParseTypes("")
	if err != nil || !reflect.DeepEqual(all, Types) {
		t.Errorf("empty = %v, %v; want every type", all, err)
	}
	got, err := ParseTypes(" code , data,code")
	if err != nil || !reflect.DeepEqual(got, []Type{TypeCode, TypeData}) {
		t.Errorf("ParseTypes = %v, %v; want [code data] deduplicated in order", got, err)
	}
	var ve *ValidationError
	if _, err := ParseTypes("data,pipelines"); !errors.As(err, &ve) || ve.Field != "types" {
		t.Errorf("unknown type: %v", err)
	}
}

func TestClampLimit(t *testing.T) {
	for in, want := range map[int]int{-5: DefaultLimit, 0: DefaultLimit, 1: 1, 200: 200, 201: MaxLimit, 1 << 20: MaxLimit} {
		if got := ClampLimit(in); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestMatchesAll(t *testing.T) {
	terms := Terms("  Order  PAY ")
	if !reflect.DeepEqual(terms, []string{"order", "pay"}) {
		t.Fatalf("Terms = %v", terms)
	}
	cases := []struct {
		fields []string
		want   bool
	}{
		{[]string{"Sales Orders", "payments"}, true}, // each term may match a different field
		{[]string{"Sales ORDERS PAYMENTS"}, true},
		{[]string{"Sales Orders"}, false}, // "pay" nowhere
		{nil, false},
	}
	for _, tc := range cases {
		if got := MatchesAll(terms, tc.fields...); got != tc.want {
			t.Errorf("MatchesAll(%v) = %v, want %v", tc.fields, got, tc.want)
		}
	}
	if !MatchesAll(nil, "anything") {
		t.Error("no terms should match everything")
	}
}

func TestLikePattern(t *testing.T) {
	for in, want := range map[string]string{"abc": "%abc%", "100%": `%100\%%`, "a_b": `%a\_b%`, "c:" + "\\" + "d": "%c:" + "\\\\" + "d%", "": "%%"} {
		if got := LikePattern(in); got != want {
			t.Errorf("LikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestText(t *testing.T) {
	if got, err := Text("f", "  hi  ", 10, true, false); err != nil || got != "hi" {
		t.Errorf("trim: %q, %v", got, err)
	}
	if _, err := Text("f", "   ", 10, true, false); err == nil {
		t.Error("required but blank was accepted")
	}
	if got, err := Text("f", "", 10, false, false); err != nil || got != "" {
		t.Errorf("optional blank: %q, %v", got, err)
	}
	// Length is in characters, not bytes.
	if _, err := Text("f", strings.Repeat("é", 10), 10, false, false); err != nil {
		t.Errorf("10 two-byte characters rejected by a 10-character limit: %v", err)
	}
	if _, err := Text("f", strings.Repeat("é", 11), 10, false, false); err == nil {
		t.Error("11 characters accepted by a 10-character limit")
	}
	if _, err := Text("f", "a\nb", 10, false, false); err == nil {
		t.Error("newline accepted in a single-line field")
	}
	if got, err := Text("f", "a\nb\tc", 10, false, true); err != nil || got != "a\nb\tc" {
		t.Errorf("multiline: %q, %v", got, err)
	}
	if _, err := Text("f", "a\x00b", 10, false, true); err == nil {
		t.Error("NUL accepted (Postgres TEXT cannot store it)")
	}
	if _, err := Text("f", "bad\xffutf8", 10, false, false); err == nil {
		t.Error("invalid UTF-8 accepted")
	}
}

func TestNormalizeLocation(t *testing.T) {
	ok := map[string]string{
		"":                "",
		"a":               "a",
		"a/b/":            "a/b",
		"a/b c/d.parquet": "a/b c/d.parquet",
		"100%/_x":         "100%/_x",
		"/":               "",
	}
	for in, want := range ok {
		got, err := NormalizeLocation("location", Location{BackendID: "lake", Path: in})
		if err != nil || got.Path != want || got.BackendID != "lake" {
			t.Errorf("NormalizeLocation(%q) = %+v, %v; want path %q", in, got, err, want)
		}
	}
	for _, in := range []string{"//", "/a", "a//b", "a/./b", "..", "a/..", `a\b`, "a\x00", strings.Repeat("a", 1025)} {
		if _, err := NormalizeLocation("location", Location{BackendID: "lake", Path: in}); err == nil {
			t.Errorf("path %q accepted", in)
		}
	}
	for _, id := range []string{"", "-a", "a-", "A", "a_b", "a/b", strings.Repeat("a", 64)} {
		if _, err := NormalizeLocation("location", Location{BackendID: id}); err == nil {
			t.Errorf("backend id %q accepted", id)
		}
	}
	for _, id := range []string{"a", "lake", "my-lake-2", strings.Repeat("a", 63)} {
		if _, err := NormalizeLocation("location", Location{BackendID: id}); err != nil {
			t.Errorf("backend id %q rejected: %v", id, err)
		}
	}
}

// The rule that ties "a location a dashboard reads" to "a location someone registered".
func TestPathContains(t *testing.T) {
	cases := []struct {
		container, p string
		want         bool
	}{
		{"warehouse/orders", "warehouse/orders", true},
		{"warehouse/orders", "warehouse/orders/2026/p.parquet", true},
		{"warehouse/orders", "warehouse/orders-archive", false}, // shares a prefix, not a folder boundary
		{"warehouse/orders", "warehouse", false},                // the reader is wider than the dataset
		{"warehouse/orders", "other/warehouse/orders", false},
		{"", "anything/at/all", true}, // the backend root contains everything
		{"", "", true},
		{"a", "", false},
	}
	for _, tc := range cases {
		if got := PathContains(tc.container, tc.p); got != tc.want {
			t.Errorf("PathContains(%q, %q) = %v, want %v", tc.container, tc.p, got, tc.want)
		}
	}
}

func TestSortHits(t *testing.T) {
	hits := []Hit{
		{Type: TypeData, ID: "5", Name: "zebra", NameMatch: false},
		{Type: TypeCode, ID: "2", Name: "Beta", NameMatch: true},
		{Type: TypeData, ID: "3", Name: "alpha", NameMatch: false},
		{Type: TypeData, ID: "1", Name: "alpha", NameMatch: true},
		{Type: TypeCode, ID: "4", Name: "alpha", NameMatch: false},
	}
	SortHits(hits)
	var got []string
	for _, h := range hits {
		got = append(got, h.ID)
	}
	// name matches first (alpha < Beta case-insensitively), then the rest by name, type, ID.
	if want := []string{"1", "2", "4", "3", "5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestValidWorkspace(t *testing.T) {
	for ws, want := range map[string]bool{"acme": true, "acme-analytics-2": true, "": false, "Acme": false, "a b": false, "a/b": false, strings.Repeat("a", 64): false} {
		if got := ValidWorkspace(ws); got != want {
			t.Errorf("ValidWorkspace(%q) = %v, want %v", ws, got, want)
		}
	}
}
