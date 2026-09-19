// Package api is booth-catalog's HTTP surface, reached through booth-core's gateway at
// /modules/catalog/* once installed (the gateway strips that prefix, so routes here start at
// /api/...).
//
// Two tiers, matching ADR 0025's workspace roles and docs/decisions/0004:
//
//   - Reading (any role): browse and search datasets, code and dashboards, read code source,
//     and read lineage.
//   - Writing (editor, owner): register/edit/delete datasets, create/edit/delete code entries
//     and publish versions. Viewers get 403 from the server whatever the UI shows.
//
// There is deliberately no write API for dashboards. They exist only because a dashboard
// module published an event about them (ADR 0018, internal/events); a route that created one
// here would be a second, competing source of truth.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/projectbooth/booth-catalog/internal/app"
	"github.com/projectbooth/booth-catalog/internal/asset"
	"github.com/projectbooth/booth-catalog/internal/auth"
	"github.com/projectbooth/booth-catalog/internal/code"
	"github.com/projectbooth/booth-catalog/internal/dashboards"
	"github.com/projectbooth/booth-catalog/internal/data"
	"github.com/projectbooth/booth-catalog/internal/events"
	"github.com/projectbooth/booth-catalog/internal/search"
)

// maxJSONBody bounds ordinary request bodies: a dataset with the maximum 1000-column schema is
// well under this.
const maxJSONBody = 512 << 10

// EventStatus reports the dashboard-event subscription's state (*events.Subscriber).
type EventStatus interface {
	Status() events.Status
}

// Deps is everything the HTTP layer needs, assembled by cmd/catalog/main.go.
type Deps struct {
	Verifier auth.TokenVerifier
	Catalog  *app.Services
	// Events is nil when the event subscription is disabled (no NATS URL configured).
	Events EventStatus
}

func NewRouter(deps Deps) http.Handler {
	s := &server{deps}

	r := chi.NewRouter()
	r.Use(middleware.Logger) // stdout logging only, per ADR 0022 — no logging API to integrate against
	r.Use(middleware.Recoverer)
	r.Use(apiHeaders)

	// Unauthenticated. /healthz is the healthCheckPath declared in this module's BoothModule
	// manifest (contracts/module-manifest.md) for booth-core to poll, so it reflects real
	// readiness (can we reach our database?). /livez is for the kubelet's liveness probe and
	// deliberately does not: restarting the pod because Postgres blipped only prolongs an outage.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/livez", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(deps.Verifier))

		r.Group(func(r chi.Router) {
			r.Use(auth.Require(auth.Identity.CanRead, "your workspace role does not permit reading the catalog"))

			r.Get("/api/config", s.handleConfig)
			r.Get("/api/search", s.handleSearch)

			r.Get("/api/datasets", s.handleListDatasets)
			r.Get("/api/datasets/{id}", s.handleGetDataset)
			r.Get("/api/datasets/{id}/lineage", s.handleDatasetLineage)
			r.Get("/api/tags", s.handleTags)

			r.Get("/api/code", s.handleListCode)
			r.Get("/api/code/{id}", s.handleGetCode)
			r.Get("/api/code/{id}/versions", s.handleCodeVersions)
			r.Get("/api/code/{id}/versions/{version}", s.handleGetCodeVersion)

			r.Get("/api/dashboards", s.handleListDashboards)
			r.Get("/api/dashboards/{id}", s.handleGetDashboard)
		})

		r.Group(func(r chi.Router) {
			r.Use(auth.Require(auth.Identity.CanWrite, "only workspace editors and owners can change the catalog"))

			r.Post("/api/datasets", s.handleCreateDataset)
			r.Put("/api/datasets/{id}", s.handleUpdateDataset)
			r.Delete("/api/datasets/{id}", s.handleDeleteDataset)

			r.Post("/api/code", s.handleCreateCode)
			r.Put("/api/code/{id}", s.handleUpdateCode)
			r.Delete("/api/code/{id}", s.handleDeleteCode)
			r.Post("/api/code/{id}/versions", s.handlePublishCodeVersion)
		})
	})

	return r
}

type server struct{ Deps }

// apiHeaders marks every response as uncacheable and un-sniffable: the bodies are
// authenticated, workspace-scoped JSON, and the code catalog serves user-supplied text.
func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// ---- health ------------------------------------------------------------------

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.Catalog.Ping(ctx); err != nil {
		log.Printf("health check failed: database unreachable: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unhealthy", "checks": map[string]string{"database": "unreachable"}})
		return
	}

	// The status CODE follows the database only. A NATS outage must not fail the readiness
	// probe: that would pull the pod out of rotation and take browsing and search down with
	// it, for a fault that only makes *dashboards* go stale. It is reported in the body, as
	// "degraded", so it is visible to anyone (or anything) reading it.
	checks := map[string]string{"database": "ok"}
	status := "ok"
	if s.Events == nil {
		checks["eventBus"] = "disabled"
	} else {
		st := s.Events.Status()
		checks["eventBus"] = string(st.State)
		if st.State != events.StateSubscribed {
			status = "degraded"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "checks": checks})
}

// handleConfig tells the UI the limits it should enforce up front, and whether dashboards can
// currently be expected to be fresh.
func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"maxCodeSourceBytes": s.Catalog.Code.MaxSourceBytes(), "dashboardEvents": map[string]string{"state": "disabled"}}
	if s.Events != nil {
		st := s.Events.Status()
		out["dashboardEvents"] = map[string]string{"state": string(st.State), "detail": st.Detail}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- search ------------------------------------------------------------------

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	types, err := asset.ParseTypes(q.Get("types"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, _, ok := paging(w, q)
	if !ok {
		return
	}
	res, err := s.Catalog.Search.Search(r.Context(), identity(r).Workspace, q.Get("q"), types, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- datasets ----------------------------------------------------------------

var datasetResource = resource{noun: "dataset", conflictField: "name", conflict: "a dataset with that name already exists in this workspace"}

func (s *server) handleListDatasets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset, ok := paging(w, q)
	if !ok {
		return
	}
	text, err := search.CheckQuery(q.Get("q"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.Catalog.Data.List(r.Context(), identity(r).Workspace, data.ListFilter{
		Query: text, Tags: q["tag"], Owner: q.Get("owner"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeFailure(w, err, datasetResource)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) handleGetDataset(w http.ResponseWriter, r *http.Request) {
	d, err := s.Catalog.Data.Get(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"))
	if err != nil {
		writeFailure(w, err, datasetResource)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleCreateDataset(w http.ResponseWriter, r *http.Request) {
	var in data.Input
	if !decodeBody(w, r, maxJSONBody, &in) {
		return
	}
	d, err := s.Catalog.Data.Create(r.Context(), identity(r).Workspace, actor(r), in)
	if err != nil {
		writeFailure(w, err, datasetResource)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *server) handleUpdateDataset(w http.ResponseWriter, r *http.Request) {
	var in data.Input
	if !decodeBody(w, r, maxJSONBody, &in) {
		return
	}
	d, err := s.Catalog.Data.Update(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"), in)
	if err != nil {
		writeFailure(w, err, datasetResource)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleDeleteDataset(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	dsID := chi.URLParam(r, "id")
	if err := s.Catalog.Data.Delete(r.Context(), id.Workspace, dsID); err != nil {
		writeFailure(w, err, datasetResource)
		return
	}
	// Removing a catalog entry never touches the data in storage, but it does change what
	// dashboards' lineage resolves to — worth a trail.
	log.Printf("audit: dataset deleted workspace=%s id=%s by=%s", id.Workspace, dsID, id.Subject)
	w.WriteHeader(http.StatusNoContent)
}

// handleDatasetLineage is the downstream half of lineage: the dashboards that read this
// dataset. (The upstream half — what a dashboard reads — is on the dashboard's own detail.)
func (s *server) handleDatasetLineage(w http.ResponseWriter, r *http.Request) {
	ws := identity(r).Workspace
	d, err := s.Catalog.Data.Get(r.Context(), ws, chi.URLParam(r, "id"))
	if err != nil {
		writeFailure(w, err, datasetResource)
		return
	}
	ds, err := s.Catalog.Dashboards.ReadingDataset(r.Context(), ws, d.ID, d.Location)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": ds})
}

func (s *server) handleTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Catalog.Data.Tags(r.Context(), identity(r).Workspace)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// ---- code --------------------------------------------------------------------

var (
	codeResource    = resource{noun: "code entry", conflictField: "name", conflict: "a code entry with that name already exists in this workspace"}
	versionResource = resource{noun: "code entry or version", conflictField: "version", conflict: "that version is already published; published versions are immutable, so publish a new version instead"}
)

// codeBodyLimit sizes a code request body: the source limit plus headroom for the other
// fields, times the worst case JSON escaping (a control character becomes six bytes).
func (s *server) codeBodyLimit() int64 { return int64(s.Catalog.Code.MaxSourceBytes())*6 + 64<<10 }

func (s *server) handleListCode(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset, ok := paging(w, q)
	if !ok {
		return
	}
	text, err := search.CheckQuery(q.Get("q"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.Catalog.Code.List(r.Context(), identity(r).Workspace, code.ListFilter{
		Query: text, Owner: q.Get("owner"), Language: q.Get("language"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeFailure(w, err, codeResource)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) handleGetCode(w http.ResponseWriter, r *http.Request) {
	e, err := s.Catalog.Code.Get(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"))
	if err != nil {
		writeFailure(w, err, codeResource)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) handleCreateCode(w http.ResponseWriter, r *http.Request) {
	var in code.CreateInput
	if !decodeBody(w, r, s.codeBodyLimit(), &in) {
		return
	}
	e, err := s.Catalog.Code.Create(r.Context(), identity(r).Workspace, actor(r), in)
	if err != nil {
		writeFailure(w, err, codeResource)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *server) handleUpdateCode(w http.ResponseWriter, r *http.Request) {
	var in code.EntryInput
	if !decodeBody(w, r, maxJSONBody, &in) {
		return
	}
	e, err := s.Catalog.Code.Update(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"), in)
	if err != nil {
		writeFailure(w, err, codeResource)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) handleDeleteCode(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	entryID := chi.URLParam(r, "id")
	if err := s.Catalog.Code.Delete(r.Context(), id.Workspace, entryID); err != nil {
		writeFailure(w, err, codeResource)
		return
	}
	log.Printf("audit: code entry deleted (with all versions) workspace=%s id=%s by=%s", id.Workspace, entryID, id.Subject)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handlePublishCodeVersion(w http.ResponseWriter, r *http.Request) {
	var in code.VersionInput
	if !decodeBody(w, r, s.codeBodyLimit(), &in) {
		return
	}
	v, err := s.Catalog.Code.Publish(r.Context(), identity(r).Workspace, actor(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeFailure(w, err, versionResource)
		return
	}
	// The response is the version's record; the caller already has the source it sent.
	writeJSON(w, http.StatusCreated, v.VersionSummary)
}

func (s *server) handleCodeVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Catalog.Code.Versions(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"))
	if err != nil {
		writeFailure(w, err, codeResource)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

func (s *server) handleGetCodeVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.Catalog.Code.GetVersion(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"), chi.URLParam(r, "version"))
	if err != nil {
		writeFailure(w, err, versionResource)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- dashboards (read-only) ----------------------------------------------------

var dashboardResource = resource{noun: "dashboard"}

func (s *server) handleListDashboards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset, ok := paging(w, q)
	if !ok {
		return
	}
	text, err := search.CheckQuery(q.Get("q"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.Catalog.Dashboards.List(r.Context(), identity(r).Workspace, dashboards.ListFilter{
		Query: text, Owner: q.Get("owner"), SourceModule: q.Get("source"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeFailure(w, err, dashboardResource)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.Catalog.Dashboards.Get(r.Context(), identity(r).Workspace, chi.URLParam(r, "id"))
	if err != nil {
		writeFailure(w, err, dashboardResource)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---- helpers -------------------------------------------------------------------

func identity(r *http.Request) auth.Identity {
	id, _ := auth.FromContext(r.Context()) // Middleware guarantees it on every route that reaches a handler
	return id
}

func actor(r *http.Request) asset.Actor {
	id := identity(r)
	return asset.Actor{Subject: id.Subject, DisplayName: id.DisplayName}
}

// paging reads ?limit and ?offset. A malformed value is the caller's mistake and is refused
// rather than silently replaced by a default. limit 0 (absent) means the default page size;
// asset.ClampLimit in the stores applies the maximum.
func paging(w http.ResponseWriter, q map[string][]string) (limit, offset int, ok bool) {
	get := func(name string) (string, bool) {
		if v := q[name]; len(v) > 0 {
			return v[0], true
		}
		return "", false
	}
	if v, present := get("limit"); present {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, asset.Invalid("limit", "must be a positive integer"))
			return 0, 0, false
		}
		limit = n
	}
	if v, present := get("offset"); present {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, asset.Invalid("offset", "must be a non-negative integer"))
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{Error: "request body too large"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid request body: " + err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
}

// resource names what a handler operates on, so the same store errors read naturally in each
// route's response ("code entry not found", "that version is already published").
type resource struct {
	noun          string
	conflictField string
	conflict      string
}

// writeFailure maps a service error onto an HTTP status for a route about res. Anything that
// isn't one of the caller-fixable cases is logged in full and reported generically: a
// database error's text can name tables and hosts, which is nothing a caller should see.
func writeFailure(w http.ResponseWriter, err error, res resource) {
	var ve *asset.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: ve.Message, Field: ve.Field})
	case errors.Is(err, asset.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: res.noun + " not found"})
	case errors.Is(err, asset.ErrExists):
		writeJSON(w, http.StatusConflict, errorBody{Error: res.conflict, Field: res.conflictField})
	case errors.Is(err, context.Canceled):
		w.WriteHeader(499) // the client went away; nothing to tell anyone
	default:
		log.Printf("catalog error: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}

// writeError is writeFailure for routes with no resource-specific wording.
func writeError(w http.ResponseWriter, err error) { writeFailure(w, err, resource{noun: "resource"}) }
