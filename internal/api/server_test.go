package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/projectbooth/booth-catalog/internal/app"
	"github.com/projectbooth/booth-catalog/internal/auth"
	"github.com/projectbooth/booth-catalog/internal/code"
	"github.com/projectbooth/booth-catalog/internal/dashboards"
	"github.com/projectbooth/booth-catalog/internal/data"
	"github.com/projectbooth/booth-catalog/internal/events"
)

// stubVerifier stands in for the OIDC provider: the tokens below are the identities the tests
// act as. Role derivation is still the real auth.Middleware — that is what's under test.
type stubVerifier map[string]*auth.Claims

func (s stubVerifier) Verify(_ context.Context, tok string) (*auth.Claims, error) {
	if c, ok := s[tok]; ok {
		return c, nil
	}
	return nil, errors.New("bad token")
}

var tokens = stubVerifier{
	"owner":  {Subject: "sub-olive", DisplayName: "olive", Groups: []string{"/workspaces/acme/owner"}},
	"editor": {Subject: "sub-ed", DisplayName: "ed@example.com", Groups: []string{"/workspaces/acme/editor"}},
	"viewer": {Subject: "sub-vic", DisplayName: "vic", Groups: []string{"/workspaces/acme/viewer"}},
	"globex": {Subject: "sub-gina", DisplayName: "gina", Groups: []string{"/workspaces/globex/editor"}},
}

type fakeEvents struct{ st events.Status }

func (f fakeEvents) Status() events.Status { return f.st }

type env struct {
	t   *testing.T
	svc *app.Services
	h   http.Handler
}

func newEnv(t *testing.T, ev EventStatus) *env {
	t.Helper()
	svc := app.NewMemory(2048) // a small source limit so the limit is testable
	return &env{t: t, svc: svc, h: NewRouter(Deps{Verifier: tokens, Catalog: svc, Events: ev})}
}

type resp struct {
	*httptest.ResponseRecorder
}

func (r resp) json(v any) {
	r.Result()
	if err := json.Unmarshal(r.Body.Bytes(), v); err != nil {
		panic("response is not JSON: " + r.Body.String())
	}
}

func (r resp) obj() map[string]any {
	var m map[string]any
	r.json(&m)
	return m
}

// do calls the API as token in workspace ws ("" sends no workspace header).
func (e *env) do(method, path, token, ws string, body any) resp {
	e.t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case string:
		rdr = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ws != "" {
		req.Header.Set(auth.HeaderBoothWorkspace, ws)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return resp{rec}
}

func (e *env) as(token, method, path string, body any) resp {
	return e.do(method, path, token, "acme", body)
}

func (e *env) want(r resp, status int) resp {
	e.t.Helper()
	if r.Code != status {
		e.t.Fatalf("status = %d, want %d; body: %s", r.Code, status, r.Body)
	}
	return r
}

func str(v any) string { s, _ := v.(string); return s }

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var orders = map[string]any{
	"name": "orders", "description": "One row per order",
	"location": map[string]any{"backendId": "lake", "path": "warehouse/orders"},
	"schema":   []map[string]any{{"name": "id", "type": "bigint"}},
	"tags":     []string{"finance", "PII"},
}

func (e *env) createDataset(token string, body map[string]any) map[string]any {
	e.t.Helper()
	return e.want(e.as(token, "POST", "/api/datasets", body), 201).obj()
}

// ---- authentication and roles --------------------------------------------------

func TestAuth_EveryCatalogRouteRequiresAToken(t *testing.T) {
	e := newEnv(t, nil)
	routes := []string{
		"GET /api/config", "GET /api/search?q=x", "GET /api/datasets", "GET /api/datasets/x", "GET /api/datasets/x/lineage", "GET /api/tags",
		"GET /api/code", "GET /api/code/x", "GET /api/code/x/versions", "GET /api/code/x/versions/1",
		"GET /api/dashboards", "GET /api/dashboards/x",
		"POST /api/datasets", "PUT /api/datasets/x", "DELETE /api/datasets/x",
		"POST /api/code", "PUT /api/code/x", "DELETE /api/code/x", "POST /api/code/x/versions",
	}
	for _, rt := range routes {
		parts := strings.SplitN(rt, " ", 2)
		if got := e.do(parts[0], parts[1], "", "acme", nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d, want 401", rt, got)
		}
		if got := e.do(parts[0], parts[1], "nonsense", "acme", nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s with a bad token: %d, want 401", rt, got)
		}
	}
	if got := e.do("GET", "/api/datasets", "viewer", "", nil).Code; got != http.StatusBadRequest {
		t.Errorf("no workspace header: %d, want 400", got)
	}
	if got := e.do("GET", "/api/datasets", "viewer", "globex", nil).Code; got != http.StatusForbidden {
		t.Errorf("a token with no role in the requested workspace: %d, want 403", got)
	}
}

func TestRoles_ViewersReadEditorsAndOwnersWrite(t *testing.T) {
	e := newEnv(t, nil)
	ds := e.createDataset("editor", orders)
	entry := e.want(e.as("editor", "POST", "/api/code", map[string]any{"name": "f", "version": "1", "source": "x"}), 201).obj()
	dsID, codeID := str(ds["id"]), str(entry["id"])

	writes := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/datasets", orders},
		{"PUT", "/api/datasets/" + dsID, orders},
		{"DELETE", "/api/datasets/" + dsID, nil},
		{"POST", "/api/code", map[string]any{"name": "g", "version": "1", "source": "x"}},
		{"PUT", "/api/code/" + codeID, map[string]any{"name": "f"}},
		{"DELETE", "/api/code/" + codeID, nil},
		{"POST", "/api/code/" + codeID + "/versions", map[string]any{"version": "2", "source": "x"}},
	}
	for _, w := range writes {
		if got := e.as("viewer", w.method, w.path, w.body).Code; got != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d, want 403", w.method, w.path, got)
		}
	}
	// Nothing above changed anything.
	if got := e.want(e.as("viewer", "GET", "/api/datasets", nil), 200).obj(); got["total"] != float64(1) {
		t.Errorf("a refused viewer write changed the catalog: %v", got)
	}

	// The reads a viewer is entitled to, all of them.
	for _, p := range []string{"/api/datasets", "/api/datasets/" + dsID, "/api/datasets/" + dsID + "/lineage", "/api/tags", "/api/code", "/api/code/" + codeID,
		"/api/code/" + codeID + "/versions", "/api/code/" + codeID + "/versions/1", "/api/code/" + codeID + "/versions/latest", "/api/dashboards", "/api/search?q=orders", "/api/config"} {
		if got := e.as("viewer", "GET", p, nil).Code; got != http.StatusOK {
			t.Errorf("viewer GET %s: %d, want 200", p, got)
		}
	}
	// Owners can write too.
	if got := e.as("owner", "POST", "/api/datasets", map[string]any{"name": "by_owner", "location": map[string]any{"backendId": "lake", "path": "x"}}).Code; got != http.StatusCreated {
		t.Errorf("owner create: %d", got)
	}
}

// ADR 0041: a viewer's token plus a forged X-Booth-Role must not reach a write route. This
// is the gap booth-storage's first pass measured against a real deployment.
func TestRoles_AForgedRoleHeaderCannotUpgradeAToken(t *testing.T) {
	e := newEnv(t, nil)
	for _, forged := range []string{"owner", "editor"} {
		req := httptest.NewRequest("POST", "/api/datasets", bytes.NewReader(mustJSON(orders)))
		req.Header.Set("Authorization", "Bearer viewer")
		req.Header.Set(auth.HeaderBoothWorkspace, "acme")
		req.Header.Set(auth.HeaderBoothRole, forged)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer token + X-Booth-Role: %s => %d, want 403", forged, rec.Code)
		}
	}
	if got := e.want(e.as("viewer", "GET", "/api/datasets", nil), 200).obj(); got["total"] != float64(0) {
		t.Errorf("a forged header created data: %v", got)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestResponsesAreNotCacheableOrSniffable(t *testing.T) {
	e := newEnv(t, nil)
	for _, r := range []resp{e.as("viewer", "GET", "/api/datasets", nil), e.do("GET", "/healthz", "", "", nil), e.do("GET", "/api/datasets", "", "acme", nil)} {
		if r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers = %v", r.Header())
		}
	}
}

// ---- health --------------------------------------------------------------------

type failingPing struct{ data.Store }

func (failingPing) Ping(context.Context) error { return errors.New("connection refused") }

func TestHealth(t *testing.T) {
	t.Run("livez is unauthenticated and never looks at the database", func(t *testing.T) {
		svc := app.New(app.Stores{Data: failingPing{data.NewMemoryStore()}, Code: code.NewMemoryStore(), Dashboards: dashboards.NewMemoryStore()}, 0)
		h := NewRouter(Deps{Verifier: tokens, Catalog: svc})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/livez", nil))
		if rec.Code != 200 {
			t.Errorf("/livez = %d; restarting the pod can't fix a database outage", rec.Code)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusServiceUnavailable || body["status"] != "unhealthy" {
			t.Errorf("/healthz with the database down = %d %v, want 503 unhealthy", rec.Code, body)
		}
	})

	cases := []struct {
		name       string
		ev         EventStatus
		wantStatus string
		wantBus    string
	}{
		{"event subscription disabled", nil, "ok", "disabled"},
		{"subscribed", fakeEvents{events.Status{State: events.StateSubscribed}}, "ok", "subscribed"},
		{"waiting for the stream", fakeEvents{events.Status{State: events.StateWaitingForStream}}, "degraded", "waiting-for-stream"},
		{"NATS unreachable", fakeEvents{events.Status{State: events.StateError, Detail: "dial tcp: refused"}}, "degraded", "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.ev)
			// A NATS problem degrades the report but must NOT fail readiness: that would pull the
			// pod out of rotation and take dataset browsing down for a fault that only stales dashboards.
			r := e.want(e.do("GET", "/healthz", "", "", nil), 200).obj()
			checks, _ := r["checks"].(map[string]any)
			if r["status"] != tc.wantStatus || checks["database"] != "ok" || checks["eventBus"] != tc.wantBus {
				t.Errorf("/healthz = %v, want status %q eventBus %q", r, tc.wantStatus, tc.wantBus)
			}
		})
	}
}

func TestConfig(t *testing.T) {
	e := newEnv(t, fakeEvents{events.Status{State: events.StateError, Detail: "nats down"}})
	r := e.want(e.as("viewer", "GET", "/api/config", nil), 200).obj()
	de, _ := r["dashboardEvents"].(map[string]any)
	if r["maxCodeSourceBytes"] != float64(2048) || de["state"] != "error" || de["detail"] != "nats down" {
		t.Errorf("config = %v", r)
	}
	r = e.want(newEnv(t, nil).as("viewer", "GET", "/api/config", nil), 200).obj()
	if de, _ = r["dashboardEvents"].(map[string]any); de["state"] != "disabled" {
		t.Errorf("config with no event bus = %v", r)
	}
}

// ---- datasets --------------------------------------------------------------------

func TestDatasets_CreateReturnsTheNormalizedRecord(t *testing.T) {
	e := newEnv(t, nil)
	d := e.createDataset("editor", orders)

	// The wire shape the UI and other modules depend on.
	wantKeys := []string{"createdAt", "createdBy", "description", "id", "location", "name", "owner", "schema", "tags", "updatedAt"}
	if got := sortedKeys(d); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("dataset keys = %v, want %v (no workspace leak)", got, wantKeys)
	}
	if d["owner"] != "ed@example.com" || d["createdBy"] != "sub-ed" {
		t.Errorf("owner/createdBy = %v/%v; owner defaults to the registering user's display name, createdBy is the token subject", d["owner"], d["createdBy"])
	}
	if !reflect.DeepEqual(d["tags"], []any{"finance", "pii"}) {
		t.Errorf("tags = %v, want normalized to lowercase and sorted", d["tags"])
	}
	loc, _ := d["location"].(map[string]any)
	if loc["backendId"] != "lake" || loc["path"] != "warehouse/orders" {
		t.Errorf("location = %v", loc)
	}

	got := e.want(e.as("viewer", "GET", "/api/datasets/"+str(d["id"]), nil), 200).obj()
	if !reflect.DeepEqual(got, d) {
		t.Errorf("GET after POST differs:\n got %v\nwant %v", got, d)
	}
}

func TestDatasets_ValidationAndConflictShapes(t *testing.T) {
	e := newEnv(t, nil)
	e.createDataset("editor", orders)

	cases := []struct {
		name   string
		body   any
		status int
		field  string
	}{
		{"missing name", map[string]any{"location": map[string]any{"backendId": "lake", "path": ""}}, 422, "name"},
		{"path traversal", map[string]any{"name": "x", "location": map[string]any{"backendId": "lake", "path": "../etc"}}, 422, "location.path"},
		{"bad backend id", map[string]any{"name": "x", "location": map[string]any{"backendId": "Lake!", "path": "a"}}, 422, "location.backendId"},
		{"bad tag", map[string]any{"name": "x", "location": map[string]any{"backendId": "lake", "path": "a"}, "tags": []string{"two words"}}, 422, "tags"},
		{"duplicate name", orders, 409, "name"},
		{"unknown field", map[string]any{"name": "x", "location": map[string]any{"backendId": "lake", "path": "a"}, "colour": "red"}, 400, ""},
		{"not json", "{nope", 400, ""},
		{"wrong type", map[string]any{"name": 42}, 400, ""},
	}
	for _, tc := range cases {
		r := e.as("editor", "POST", "/api/datasets", tc.body)
		if r.Code != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, r.Code, tc.status, r.Body)
			continue
		}
		body := r.obj()
		if str(body["error"]) == "" {
			t.Errorf("%s: no error message in %v", tc.name, body)
		}
		if str(body["field"]) != tc.field {
			t.Errorf("%s: field = %q, want %q", tc.name, body["field"], tc.field)
		}
	}
	// A body far over the limit is refused, not buffered.
	huge := `{"name":"x","description":"` + strings.Repeat("d", 600<<10) + `"}`
	if got := e.as("editor", "POST", "/api/datasets", huge).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d, want 413", got)
	}
}

func TestDatasets_UpdateDeleteAndUnknownIDs(t *testing.T) {
	e := newEnv(t, nil)
	d := e.createDataset("editor", orders)
	id := str(d["id"])

	upd := map[string]any{"name": "orders_v2", "description": "renamed", "location": map[string]any{"backendId": "lake", "path": "warehouse/orders_v2"}}
	got := e.want(e.as("owner", "PUT", "/api/datasets/"+id, upd), 200).obj()
	if got["name"] != "orders_v2" || got["owner"] != "ed@example.com" {
		t.Errorf("after update: %v (an update that names no owner must keep the existing one, not take it)", got)
	}

	other := e.createDataset("editor", map[string]any{"name": "other", "location": map[string]any{"backendId": "lake", "path": "o"}})
	clash := map[string]any{"name": "orders_v2", "location": map[string]any{"backendId": "lake", "path": "o"}}
	e.want(e.as("editor", "PUT", "/api/datasets/"+str(other["id"]), clash), 409)

	e.want(e.as("editor", "PUT", "/api/datasets/nope", upd), 404)
	e.want(e.as("editor", "DELETE", "/api/datasets/nope", nil), 404)
	e.want(e.as("viewer", "GET", "/api/datasets/nope", nil), 404)

	e.want(e.as("editor", "DELETE", "/api/datasets/"+id, nil), 204)
	e.want(e.as("viewer", "GET", "/api/datasets/"+id, nil), 404)
}

func TestDatasets_ListFiltersAndPaging(t *testing.T) {
	e := newEnv(t, nil)
	mk := func(name, owner string, tags ...string) {
		e.createDataset("editor", map[string]any{"name": name, "owner": owner, "tags": tags, "location": map[string]any{"backendId": "lake", "path": name}})
	}
	mk("sales", "alice", "finance", "pii")
	mk("web", "bob", "pii")
	mk("hr", "alice", "finance")

	names := func(path string) []string {
		r := e.want(e.as("viewer", "GET", path, nil), 200).obj()
		var out []string
		for _, it := range r["items"].([]any) {
			out = append(out, str(it.(map[string]any)["name"]))
		}
		return out
	}
	for path, want := range map[string][]string{
		"/api/datasets":                     {"hr", "sales", "web"},
		"/api/datasets?q=SAL":               {"sales"},
		"/api/datasets?tag=pii":             {"sales", "web"},
		"/api/datasets?tag=pii&tag=finance": {"sales"},
		"/api/datasets?tag=PII":             {"sales", "web"},
		"/api/datasets?owner=ALICE":         {"hr", "sales"},
		"/api/datasets?owner=alice&tag=pii": {"sales"},
		"/api/datasets?limit=2":             {"hr", "sales"},
		"/api/datasets?limit=2&offset=2":    {"web"},
		"/api/datasets?q=finance":           {"hr", "sales"}, // a query matches tags too
	} {
		if got := names(path); !reflect.DeepEqual(got, want) {
			t.Errorf("GET %s = %v, want %v", path, got, want)
		}
	}
	r := e.want(e.as("viewer", "GET", "/api/datasets?limit=1", nil), 200).obj()
	if r["total"] != float64(3) {
		t.Errorf("total = %v, want the full match count", r["total"])
	}
	for _, bad := range []string{"limit=0", "limit=abc", "limit=-1", "offset=-1", "offset=x", "tag=two+words", "q=" + strings.Repeat("a", 201)} {
		if got := e.as("viewer", "GET", "/api/datasets?"+bad, nil).Code; got != 422 {
			t.Errorf("GET ?%s = %d, want 422", bad, got)
		}
	}

	tags := e.want(e.as("viewer", "GET", "/api/tags", nil), 200).obj()
	if !reflect.DeepEqual(tags["tags"], []any{map[string]any{"tag": "finance", "count": float64(2)}, map[string]any{"tag": "pii", "count": float64(2)}}) {
		t.Errorf("tags = %v", tags)
	}
}

func TestWorkspacesAreIsolatedThroughTheAPI(t *testing.T) {
	e := newEnv(t, nil)
	d := e.createDataset("editor", orders)
	e.want(e.do("POST", "/api/datasets", "globex", "globex", orders), 201) // the same name is free in another workspace

	// globex's editor can't see, edit or delete acme's dataset, even with the exact ID.
	id := str(d["id"])
	e.want(e.do("GET", "/api/datasets/"+id, "globex", "globex", nil), 404)
	e.want(e.do("PUT", "/api/datasets/"+id, "globex", "globex", orders), 404)
	e.want(e.do("DELETE", "/api/datasets/"+id, "globex", "globex", nil), 404)
	if got := e.want(e.as("viewer", "GET", "/api/datasets/"+id, nil), 200).obj(); got["name"] != "orders" {
		t.Errorf("acme's dataset was disturbed: %v", got)
	}
	// And a token from another workspace can't be pointed at acme by header alone.
	e.want(e.do("GET", "/api/datasets", "globex", "acme", nil), 403)
}

// ---- code ------------------------------------------------------------------------

func TestCode_PublishBrowseAndPinVersions(t *testing.T) {
	e := newEnv(t, nil)
	created := e.want(e.as("editor", "POST", "/api/code", map[string]any{
		"name": "clean_emails", "description": "strips whitespace", "language": "Python",
		"version": "1.0.0", "source": "def clean(x): return x.strip()", "notes": "first",
	}), 201).obj()
	id := str(created["id"])

	wantKeys := []string{"createdAt", "createdBy", "description", "id", "language", "latestVersion", "name", "owner", "updatedAt", "versionCount"}
	if got := sortedKeys(created); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("entry keys = %v, want %v", got, wantKeys)
	}
	latest, _ := created["latestVersion"].(map[string]any)
	if created["language"] != "python" || created["versionCount"] != float64(1) || latest["version"] != "1.0.0" || latest["seq"] != float64(1) {
		t.Errorf("created = %v", created)
	}
	if _, leaked := latest["source"]; leaked {
		t.Error("an entry's latestVersion summary carries the whole source; that belongs to the version route")
	}

	pub := e.want(e.as("editor", "POST", "/api/code/"+id+"/versions", map[string]any{"version": "1.1.0", "source": "def clean(x): return x.strip().lower()", "notes": "lowercase"}), 201).obj()
	if pub["version"] != "1.1.0" || pub["seq"] != float64(2) || pub["publishedBy"] != "ed@example.com" {
		t.Errorf("published = %v", pub)
	}
	if _, leaked := pub["source"]; leaked {
		t.Error("publish echoed the source back")
	}

	hist := e.want(e.as("viewer", "GET", "/api/code/"+id+"/versions", nil), 200).obj()
	vs, _ := hist["versions"].([]any)
	if len(vs) != 2 || vs[0].(map[string]any)["version"] != "1.1.0" || vs[1].(map[string]any)["version"] != "1.0.0" {
		t.Errorf("history = %v, want newest first", hist)
	}
	for _, v := range vs {
		if _, has := v.(map[string]any)["source"]; has {
			t.Error("history listing carries source")
		}
	}

	pinned := e.want(e.as("viewer", "GET", "/api/code/"+id+"/versions/1.0.0", nil), 200).obj()
	if pinned["source"] != "def clean(x): return x.strip()" || pinned["notes"] != "first" {
		t.Errorf("pinned = %v", pinned)
	}
	alias := e.want(e.as("viewer", "GET", "/api/code/"+id+"/versions/latest", nil), 200).obj()
	if alias["version"] != "1.1.0" {
		t.Errorf("latest alias = %v", alias)
	}

	// Published versions are immutable: republishing a label is a conflict, and changes nothing.
	dup := e.want(e.as("editor", "POST", "/api/code/"+id+"/versions", map[string]any{"version": "1.0.0", "source": "tampered"}), 409).obj()
	if dup["field"] != "version" || !strings.Contains(str(dup["error"]), "immutable") {
		t.Errorf("conflict body = %v", dup)
	}
	if again := e.want(e.as("viewer", "GET", "/api/code/"+id+"/versions/1.0.0", nil), 200).obj(); again["source"] != pinned["source"] {
		t.Error("a refused republish changed the published source")
	}

	e.want(e.as("viewer", "GET", "/api/code/"+id+"/versions/9.9.9", nil), 404)
	e.want(e.as("viewer", "GET", "/api/code/nope/versions/latest", nil), 404)
	e.want(e.as("editor", "POST", "/api/code/nope/versions", map[string]any{"version": "1", "source": "x"}), 404)
}

func TestCode_ValidationAndLimits(t *testing.T) {
	e := newEnv(t, nil) // source limit: 2048 bytes
	base := func(m map[string]any) map[string]any {
		out := map[string]any{"name": "f", "version": "1", "source": "x"}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name   string
		body   map[string]any
		status int
		field  string
	}{
		{"missing name", base(map[string]any{"name": ""}), 422, "name"},
		{"missing version", base(map[string]any{"version": ""}), 422, "version"},
		{"reserved version label", base(map[string]any{"version": "latest"}), 422, "version"},
		{"blank source", base(map[string]any{"source": "  "}), 422, "source"},
		{"source over the limit", base(map[string]any{"source": strings.Repeat("x", 2049)}), 422, "source"},
		{"source at the limit", base(map[string]any{"source": strings.Repeat("x", 2048)}), 201, ""},
		{"bad language", base(map[string]any{"name": "g", "language": "not a language"}), 422, "language"},
	}
	for _, tc := range cases {
		r := e.as("editor", "POST", "/api/code", tc.body)
		if r.Code != tc.status {
			t.Errorf("%s: %d, want %d (%s)", tc.name, r.Code, tc.status, r.Body)
			continue
		}
		if tc.field != "" && r.obj()["field"] != tc.field {
			t.Errorf("%s: field = %v, want %s", tc.name, r.obj()["field"], tc.field)
		}
	}
	e.want(e.as("editor", "POST", "/api/code", base(map[string]any{"name": "f"})), 409) // "f" now exists
	// A body vastly beyond anything a valid source could occupy is refused up front.
	e.want(e.as("editor", "POST", "/api/code", base(map[string]any{"name": "big", "source": strings.Repeat("x", 100<<10)})), 413)
}

// JSON escaping can inflate a control-character-heavy source well past its byte length; a
// source within the byte limit must not be refused just because of how it serializes.
func TestCode_SourceWithinTheLimitSurvivesJSONEscaping(t *testing.T) {
	e := newEnv(t, nil)
	src := strings.Repeat("\"\n\t\\", 500) // 2000 bytes; ~5000+ once escaped
	r := e.want(e.as("editor", "POST", "/api/code", map[string]any{"name": "escapey", "version": "1", "source": src}), 201).obj()
	got := e.want(e.as("viewer", "GET", "/api/code/"+str(r["id"])+"/versions/1", nil), 200).obj()
	if got["source"] != src {
		t.Error("source did not survive the round trip byte for byte")
	}
}

func TestCode_UpdateDeleteAndFilters(t *testing.T) {
	e := newEnv(t, nil)
	mk := func(name, owner, lang string) map[string]any {
		return e.want(e.as("editor", "POST", "/api/code", map[string]any{"name": name, "owner": owner, "language": lang, "version": "1", "source": "x"}), 201).obj()
	}
	a := mk("parse_dates", "alice", "python")
	mk("clean_sql", "bob", "sql")
	mk("parse_json", "alice", "sql")

	names := func(path string) []string {
		r := e.want(e.as("viewer", "GET", path, nil), 200).obj()
		var out []string
		for _, it := range r["items"].([]any) {
			out = append(out, str(it.(map[string]any)["name"]))
		}
		return out
	}
	for path, want := range map[string][]string{
		"/api/code":                      {"clean_sql", "parse_dates", "parse_json"},
		"/api/code?q=parse":              {"parse_dates", "parse_json"},
		"/api/code?owner=ALICE":          {"parse_dates", "parse_json"},
		"/api/code?language=sql":         {"clean_sql", "parse_json"},
		"/api/code?q=parse&language=sql": {"parse_json"},
		"/api/code?limit=1&offset=1":     {"parse_dates"},
	} {
		if got := names(path); !reflect.DeepEqual(got, want) {
			t.Errorf("GET %s = %v, want %v", path, got, want)
		}
	}

	upd := e.want(e.as("editor", "PUT", "/api/code/"+str(a["id"]), map[string]any{"name": "parse_dates", "description": "now documented"}), 200).obj()
	if upd["owner"] != "alice" || upd["description"] != "now documented" || upd["versionCount"] != float64(1) {
		t.Errorf("update = %v (owner kept, versions untouched)", upd)
	}
	e.want(e.as("editor", "PUT", "/api/code/"+str(a["id"]), map[string]any{"name": "clean_sql"}), 409)
	e.want(e.as("editor", "PUT", "/api/code/nope", map[string]any{"name": "x"}), 404)

	e.want(e.as("editor", "DELETE", "/api/code/"+str(a["id"]), nil), 204)
	e.want(e.as("viewer", "GET", "/api/code/"+str(a["id"]), nil), 404)
	e.want(e.as("viewer", "GET", "/api/code/"+str(a["id"])+"/versions/1", nil), 404)
	e.want(e.as("editor", "DELETE", "/api/code/"+str(a["id"]), nil), 404)
}

// ---- dashboards and lineage ---------------------------------------------------------

func applyDashboard(t *testing.T, e *env, ws, module, id, name string, at time.Time, sources ...dashboards.Source) {
	t.Helper()
	ok, err := e.svc.Dashboards.Apply(context.Background(), dashboards.Upsert{
		Workspace: ws, SourceModule: module, ExternalID: id, Name: name, Owner: "alice", Path: "/" + module + "/dashboard/" + id,
		Sources: sources, At: at,
	})
	if err != nil || !ok {
		t.Fatalf("Apply(%s) = %v, %v", name, ok, err)
	}
}

func TestDashboards_AreReadOnlyOverHTTP(t *testing.T) {
	e := newEnv(t, nil)
	applyDashboard(t, e, "acme", "superset", "42", "Revenue", time.Now())

	page := e.want(e.as("viewer", "GET", "/api/dashboards", nil), 200).obj()
	items, _ := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("dashboards = %v", page)
	}
	d := items[0].(map[string]any)
	wantKeys := []string{"createdAt", "description", "externalId", "id", "lineageComplete", "name", "owner", "path", "sourceModule", "updatedAt"}
	if got := sortedKeys(d); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("dashboard keys = %v, want %v", got, wantKeys)
	}

	// Dashboards come from events. No route may create, change or delete one — for anyone.
	id := str(d["id"])
	for _, tok := range []string{"owner", "editor"} {
		for _, rt := range [][2]string{{"POST", "/api/dashboards"}, {"PUT", "/api/dashboards/" + id}, {"DELETE", "/api/dashboards/" + id}, {"PATCH", "/api/dashboards/" + id}} {
			if got := e.as(tok, rt[0], rt[1], map[string]any{"name": "x"}).Code; got != http.StatusNotFound && got != http.StatusMethodNotAllowed {
				t.Errorf("%s %s %s = %d, want 404/405", tok, rt[0], rt[1], got)
			}
		}
	}
	e.want(e.as("viewer", "GET", "/api/dashboards/"+id, nil), 200)
	e.want(e.as("viewer", "GET", "/api/dashboards/nope", nil), 404)
	e.want(e.do("GET", "/api/dashboards/"+id, "globex", "globex", nil), 404)
}

func TestDashboards_ListFilters(t *testing.T) {
	e := newEnv(t, nil)
	at := time.Now()
	applyDashboard(t, e, "acme", "superset", "1", "Sales", at)
	applyDashboard(t, e, "acme", "metabase", "1", "Traffic", at)
	applyDashboard(t, e, "acme", "streamlit", "1", "Sales explorer", at)
	names := func(path string) []string {
		r := e.want(e.as("viewer", "GET", path, nil), 200).obj()
		var out []string
		for _, it := range r["items"].([]any) {
			out = append(out, str(it.(map[string]any)["name"]))
		}
		return out
	}
	for path, want := range map[string][]string{
		"/api/dashboards":                     {"Sales", "Sales explorer", "Traffic"},
		"/api/dashboards?q=sales":             {"Sales", "Sales explorer"},
		"/api/dashboards?source=metabase":     {"Traffic"},
		"/api/dashboards?source=superset&q=s": {"Sales"},
		"/api/dashboards?owner=ALICE&limit=1": {"Sales"},
	} {
		if got := names(path); !reflect.DeepEqual(got, want) {
			t.Errorf("GET %s = %v, want %v", path, got, want)
		}
	}
}

// Lineage end to end through the real services: a dashboard names what it reads, and the
// dataset catalog connects the two in both directions — including for a dataset registered
// AFTER the dashboard was indexed.
func TestLineage_ConnectsDashboardsAndDatasetsBothWays(t *testing.T) {
	e := newEnv(t, nil)
	at := time.Now()

	// Indexed first: nothing in the catalog yet for it to point at.
	applyDashboard(t, e, "acme", "streamlit", "app-1", "Orders explorer", at,
		dashboards.Source{Type: dashboards.SourceLocation, BackendID: "lake", Path: "warehouse/orders/2026/part-1.parquet"},
		dashboards.Source{Type: dashboards.SourceExternal, Name: "public.customers", System: "postgres"})

	dash := func() map[string]any {
		page := e.want(e.as("viewer", "GET", "/api/dashboards", nil), 200).obj()
		id := str(page["items"].([]any)[0].(map[string]any)["id"])
		return e.want(e.as("viewer", "GET", "/api/dashboards/"+id, nil), 200).obj()
	}
	sources := func(d map[string]any) []any { return d["lineage"].(map[string]any)["sources"].([]any) }

	before := dash()
	if len(sources(before)) != 2 {
		t.Fatalf("lineage = %v", before["lineage"])
	}
	for i, s := range sources(before) {
		if ds := s.(map[string]any)["datasets"].([]any); len(ds) != 0 {
			t.Errorf("source %d resolved to %v before any dataset was registered", i, ds)
		}
	}

	// Register the dataset the app reads from (its folder contains the file the app opens).
	ds := e.createDataset("editor", orders) // lake:warehouse/orders

	after := dash()
	src0 := sources(after)[0].(map[string]any)
	resolved, _ := src0["datasets"].([]any)
	if len(resolved) != 1 || resolved[0].(map[string]any)["id"] != ds["id"] || resolved[0].(map[string]any)["name"] != "orders" {
		t.Errorf("the location source resolved to %v, want the newly registered dataset", resolved)
	}
	if src0["type"] != "location" || src0["backendId"] != "lake" {
		t.Errorf("the source as published was not preserved: %v", src0)
	}
	if ext := sources(after)[1].(map[string]any); len(ext["datasets"].([]any)) != 0 {
		t.Errorf("an external source was resolved: %v", ext)
	}
	if lin := after["lineage"].(map[string]any); lin["complete"] != false {
		t.Errorf("lineage.complete = %v; a publisher that didn't claim completeness gets false", lin["complete"])
	}

	// The downstream half: the dataset knows which dashboards read it.
	down := e.want(e.as("viewer", "GET", "/api/datasets/"+str(ds["id"])+"/lineage", nil), 200).obj()
	dl, _ := down["dashboards"].([]any)
	if len(dl) != 1 || dl[0].(map[string]any)["name"] != "Orders explorer" {
		t.Errorf("downstream lineage = %v", down)
	}

	// Deleting the dataset severs the edge again (a dashboard's sources are what it published).
	e.want(e.as("editor", "DELETE", "/api/datasets/"+str(ds["id"]), nil), 204)
	if r := sources(dash())[0].(map[string]any); len(r["datasets"].([]any)) != 0 {
		t.Errorf("a deleted dataset still resolves: %v", r)
	}
	e.want(e.as("viewer", "GET", "/api/datasets/"+str(ds["id"])+"/lineage", nil), 404)
}

func TestLineage_APIShapeWhenNothingReadsTheDataset(t *testing.T) {
	e := newEnv(t, nil)
	ds := e.createDataset("editor", orders)
	down := e.want(e.as("viewer", "GET", "/api/datasets/"+str(ds["id"])+"/lineage", nil), 200).obj()
	if dl, ok := down["dashboards"].([]any); !ok || len(dl) != 0 {
		t.Errorf("lineage = %v, want {\"dashboards\": []}", down)
	}
}

// ---- search ----------------------------------------------------------------------

func TestSearch_SpansAllThreeAssetTypes(t *testing.T) {
	e := newEnv(t, nil)
	e.createDataset("editor", map[string]any{"name": "order_events", "description": "raw", "location": map[string]any{"backendId": "lake", "path": "a"}})
	e.want(e.as("editor", "POST", "/api/code", map[string]any{"name": "parse_order", "version": "1", "source": "x"}), 201)
	applyDashboard(t, e, "acme", "superset", "1", "Orders overview", time.Now())
	e.createDataset("editor", map[string]any{"name": "unrelated", "description": "holds every order", "location": map[string]any{"backendId": "lake", "path": "b"}})
	e.do("POST", "/api/datasets", "globex", "globex", map[string]any{"name": "order_elsewhere", "location": map[string]any{"backendId": "lake", "path": "c"}})

	type hit struct{ Type, Name string }
	search := func(path string) []hit {
		r := e.want(e.as("viewer", "GET", path, nil), 200)
		var out struct {
			Query string
			Hits  []map[string]any
		}
		r.json(&out)
		hs := []hit{}
		for _, h := range out.Hits {
			hs = append(hs, hit{str(h["type"]), str(h["name"])})
			if str(h["id"]) == "" {
				t.Errorf("hit without an id: %v", h)
			}
		}
		return hs
	}

	all := search("/api/search?q=ORDER")
	// Name matches first — alphabetical, case-insensitive — then the description-only match; and
	// nothing from another workspace.
	want := []hit{{"data", "order_events"}, {"dashboard", "Orders overview"}, {"code", "parse_order"}, {"data", "unrelated"}}
	if !reflect.DeepEqual(all, want) {
		t.Errorf("search = %v\nwant %v", all, want)
	}
	if got := search("/api/search?q=order&types=code"); !reflect.DeepEqual(got, []hit{{"code", "parse_order"}}) {
		t.Errorf("types=code: %v", got)
	}
	if got := search("/api/search?q=order&types=data,dashboard&limit=2"); !reflect.DeepEqual(got, []hit{{"data", "order_events"}, {"dashboard", "Orders overview"}}) {
		t.Errorf("types=data,dashboard&limit=2: %v", got)
	}
	if got := search("/api/search?q=nothingmatches"); got == nil || len(got) != 0 {
		t.Errorf("no match: %#v", got)
	}

	// A dashboard hit says which module published it.
	r := e.want(e.as("viewer", "GET", "/api/search?q=overview&types=dashboard", nil), 200).obj()
	if h := r["hits"].([]any)[0].(map[string]any); h["source"] != "superset" {
		t.Errorf("dashboard hit = %v, want its publishing module as source", h)
	}
	if r["query"] != "overview" {
		t.Errorf("query = %v", r["query"])
	}

	for _, bad := range []string{"", "q=", "q=%20", "q=x&types=pipelines", "q=x&limit=0", "q=" + strings.Repeat("w+", 11)} {
		if got := e.as("viewer", "GET", "/api/search?"+bad, nil).Code; got != 422 {
			t.Errorf("GET /api/search?%s = %d, want 422", bad, got)
		}
	}
}

func TestSearch_QueryWildcardsAreLiteral(t *testing.T) {
	e := newEnv(t, nil)
	e.createDataset("editor", map[string]any{"name": "growth", "description": "up 100% yoy", "location": map[string]any{"backendId": "lake", "path": "a"}})
	e.createDataset("editor", map[string]any{"name": "other", "location": map[string]any{"backendId": "lake", "path": "b"}})
	r := e.want(e.as("viewer", "GET", "/api/search?q=%25", nil), 200).obj() // q=%
	if hits := r["hits"].([]any); len(hits) != 1 || hits[0].(map[string]any)["name"] != "growth" {
		t.Errorf("searching for a literal %% matched %v", hits)
	}
}
