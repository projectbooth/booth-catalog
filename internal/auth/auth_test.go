package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeIdP is a minimal but real OIDC provider: a discovery document, a JWKS endpoint, and
// a signing key, so Verifier is exercised through the same go-oidc code path production
// uses rather than a stub.
type fakeIdP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"jwks_uri":                              idp.server.URL + "/jwks",
			"authorization_endpoint":                idp.server.URL + "/auth",
			"token_endpoint":                        idp.server.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

type tokenOpts struct {
	issuer   string
	subject  string
	audience string
	expiry   time.Duration
	signWith *rsa.PrivateKey
	// groups, if non-nil, is emitted as the claim named groupsClaim (default "groups").
	groups      any
	groupsClaim string
	extra       map[string]any
}

func (idp *fakeIdP) token(t *testing.T, o tokenOpts) string {
	t.Helper()
	if o.issuer == "" {
		o.issuer = idp.server.URL
	}
	if o.expiry == 0 {
		o.expiry = time.Hour
	}
	key := o.signWith
	if key == nil {
		key = idp.key
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.Claims{Issuer: o.issuer, Subject: o.subject, IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(time.Now().Add(o.expiry))}
	if o.audience != "" {
		claims.Audience = jwt.Audience{o.audience}
	}
	builder := jwt.Signed(signer).Claims(claims)
	if o.groups != nil {
		name := o.groupsClaim
		if name == "" {
			name = "groups"
		}
		builder = builder.Claims(map[string]any{name: o.groups})
	}
	if o.extra != nil {
		builder = builder.Claims(o.extra)
	}
	raw, err := builder.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifier(t *testing.T) {
	idp := newFakeIdP(t)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ctx := context.Background()

	lax, err := NewVerifier(ctx, OIDCConfig{IssuerURL: idp.server.URL, ClientID: "booth-catalog"})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	strict, err := NewVerifier(ctx, OIDCConfig{IssuerURL: idp.server.URL, ClientID: "booth-catalog", RequireAudience: true})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		verifier *Verifier
		opts     tokenOpts
		wantSub  string
		wantErr  bool
	}{
		{"valid", lax, tokenOpts{subject: "alice"}, "alice", false},
		{"valid, audience not required so any audience passes", lax, tokenOpts{subject: "alice", audience: "account"}, "alice", false},
		{"expired", lax, tokenOpts{subject: "alice", expiry: -time.Hour}, "", true},
		{"wrong issuer", lax, tokenOpts{subject: "alice", issuer: "https://evil.example"}, "", true},
		{"signed by an unknown key", lax, tokenOpts{subject: "alice", signWith: otherKey}, "", true},
		{"missing subject", lax, tokenOpts{subject: ""}, "", true},
		{"audience required and matching", strict, tokenOpts{subject: "alice", audience: "booth-catalog"}, "alice", false},
		{"audience required but wrong", strict, tokenOpts{subject: "alice", audience: "someone-else"}, "", true},
		{"audience required but absent", strict, tokenOpts{subject: "alice"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := tc.verifier.Verify(ctx, idp.token(t, tc.opts))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Verify succeeded with claims %+v, want an error", claims)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if claims.Subject != tc.wantSub {
				t.Errorf("subject = %q, want %q", claims.Subject, tc.wantSub)
			}
		})
	}

	if _, err := lax.Verify(ctx, "not.a.jwt"); err == nil {
		t.Error("garbage token verified")
	}
}

func TestNewVerifier_UnreachableIssuer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := NewVerifier(ctx, OIDCConfig{IssuerURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("NewVerifier succeeded against an unreachable issuer")
	}
}

// The display name is what a new dataset's owner defaults to: preferred_username, then
// email, then the subject (an opaque UUID on Keycloak — a last resort, never preferred).
func TestVerifier_DisplayName(t *testing.T) {
	idp := newFakeIdP(t)
	ctx := context.Background()
	v, err := NewVerifier(ctx, OIDCConfig{IssuerURL: idp.server.URL, ClientID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"username wins", map[string]any{"preferred_username": "alice", "email": "alice@example.com"}, "alice"},
		{"email when there is no username", map[string]any{"email": "alice@example.com"}, "alice@example.com"},
		{"subject as the last resort", nil, "sub-1"},
		{"blank claims are ignored", map[string]any{"preferred_username": "  ", "email": ""}, "sub-1"},
		{"a claim of the wrong type is ignored", map[string]any{"preferred_username": 42, "email": "a@b.c"}, "a@b.c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := v.Verify(ctx, idp.token(t, tokenOpts{subject: "sub-1", extra: tc.extra}))
			if err != nil {
				t.Fatal(err)
			}
			if c.DisplayName != tc.want {
				t.Errorf("DisplayName = %q, want %q", c.DisplayName, tc.want)
			}
		})
	}
}

// newFakeCore is booth-core's workload issuer as a module sees it (ADR 0056): a JWKS at the
// fixed well-known path and nothing else — in particular no OIDC discovery document, which is
// why NewWorkloadVerifier must not need one.
func newFakeCore(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc(WorkloadJWKSPath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	core.server = httptest.NewServer(mux)
	t.Cleanup(core.server.Close)
	return core
}

// A workload token verifies against core's JWKS, and its groups claim feeds the unchanged
// ADR 0041 role derivation.
func TestWorkloadVerifier(t *testing.T) {
	core := newFakeCore(t)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ctx := context.Background()

	v := NewWorkloadVerifier(ctx, OIDCConfig{IssuerURL: core.server.URL + "/", ClientID: "booth-catalog", RequireAudience: true})

	claims, err := v.Verify(ctx, core.token(t, tokenOpts{
		subject: "job:nightly-42", audience: "booth-catalog", groups: []string{"/workspaces/acme/editor"},
	}))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "job:nightly-42" {
		t.Errorf("subject = %q", claims.Subject)
	}
	if got := RoleInWorkspace(claims.Groups, "acme"); got != RoleEditor {
		t.Errorf("role = %q, want editor", got)
	}
	if got := RoleInWorkspace(claims.Groups, "other"); got != "" {
		t.Errorf("role in another workspace = %q, want none", got)
	}

	for name, o := range map[string]tokenOpts{
		"signed by an unknown key": {subject: "job:x", audience: "booth-catalog", signWith: otherKey},
		"wrong issuer":             {subject: "job:x", audience: "booth-catalog", issuer: "https://evil.example"},
		"expired":                  {subject: "job:x", audience: "booth-catalog", expiry: -time.Hour},
		"wrong audience":           {subject: "job:x", audience: "someone-else"},
	} {
		if c, err := v.Verify(ctx, core.token(t, o)); err == nil {
			t.Errorf("%s: verified with claims %+v, want an error", name, c)
		}
	}
}

// Constructing the workload verifier must not touch the network: booth-catalog must start with
// booth-core down or not yet deployed.
func TestWorkloadVerifier_NoNetworkAtConstruction(t *testing.T) {
	v := NewWorkloadVerifier(context.Background(), OIDCConfig{IssuerURL: "http://127.0.0.1:1"})
	if v == nil {
		t.Fatal("nil verifier")
	}
	if _, err := v.Verify(context.Background(), "not.a.jwt"); err == nil {
		t.Error("garbage token verified")
	}
}

// With both issuers trusted, each issuer's tokens are accepted and neither issuer's key can
// vouch for a token naming the other.
func TestChainVerifier_TwoIssuers(t *testing.T) {
	idp := newFakeIdP(t)
	core := newFakeCore(t)
	ctx := context.Background()

	human, err := NewVerifier(ctx, OIDCConfig{IssuerURL: idp.server.URL, ClientID: "booth-catalog"})
	if err != nil {
		t.Fatal(err)
	}
	chain := ChainVerifier{human, NewWorkloadVerifier(ctx, OIDCConfig{IssuerURL: core.server.URL, ClientID: "booth-catalog"})}

	if c, err := chain.Verify(ctx, idp.token(t, tokenOpts{subject: "alice", groups: []string{"/workspaces/acme/owner"}})); err != nil || c.Subject != "alice" {
		t.Errorf("human token: %+v, %v", c, err)
	}
	if c, err := chain.Verify(ctx, core.token(t, tokenOpts{subject: "job:1", groups: []string{"/workspaces/acme/viewer"}})); err != nil || c.Subject != "job:1" {
		t.Errorf("workload token: %+v, %v", c, err)
	}
	// Core's key claiming to be the human provider, and the provider's key claiming to be core.
	if c, err := chain.Verify(ctx, core.token(t, tokenOpts{subject: "mallory", issuer: idp.server.URL})); err == nil {
		t.Errorf("core-signed token naming the OIDC issuer verified: %+v", c)
	}
	if c, err := chain.Verify(ctx, idp.token(t, tokenOpts{subject: "mallory", issuer: core.server.URL})); err == nil {
		t.Errorf("provider-signed token naming core verified: %+v", c)
	}
	if _, err := (ChainVerifier{}).Verify(ctx, "x"); err == nil {
		t.Error("empty chain accepted a token")
	}
}

type stubVerifier map[string]*Claims

func (s stubVerifier) Verify(_ context.Context, tok string) (*Claims, error) {
	if c, ok := s[tok]; ok {
		return c, nil
	}
	return nil, errors.New("bad token")
}

func TestMiddleware(t *testing.T) {
	var seen Identity
	handler := Middleware(stubVerifier{"good": {Subject: "alice", DisplayName: "alice@example.com", Groups: []string{"/workspaces/acme/editor"}}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	do := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no token", map[string]string{HeaderBoothWorkspace: "acme"}, http.StatusUnauthorized},
		{"non-bearer scheme", map[string]string{"Authorization": "Basic Zm9v", HeaderBoothWorkspace: "acme"}, http.StatusUnauthorized},
		{"invalid token", map[string]string{"Authorization": "Bearer nope", HeaderBoothWorkspace: "acme"}, http.StatusUnauthorized},
		// The forwarded headers alone must never be enough — that is the whole point of
		// independently verifying the token.
		{"forged workspace/role headers without a token", map[string]string{HeaderBoothWorkspace: "acme", HeaderBoothRole: "owner"}, http.StatusUnauthorized},
		{"valid token but no workspace header", map[string]string{"Authorization": "Bearer good"}, http.StatusBadRequest},
		{"valid", map[string]string{"Authorization": "Bearer good", HeaderBoothWorkspace: "acme", HeaderBoothRole: "editor"}, http.StatusOK},
		{"scheme is case-insensitive", map[string]string{"Authorization": "bearer good", HeaderBoothWorkspace: "acme", HeaderBoothRole: "viewer"}, http.StatusOK},
		{"token grants no role in the requested workspace", map[string]string{"Authorization": "Bearer good", HeaderBoothWorkspace: "globex", HeaderBoothRole: "owner"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen = Identity{}
			rec := do(tc.headers)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
			if tc.want != http.StatusOK {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
					t.Errorf("error body = %q, want JSON {\"error\": ...}", rec.Body)
				}
				if seen != (Identity{}) {
					t.Errorf("handler ran despite rejection: %+v", seen)
				}
			}
		})
	}

	do(map[string]string{"Authorization": "Bearer good", HeaderBoothWorkspace: "acme", HeaderBoothRole: "editor"})
	if seen.Subject != "alice" || seen.DisplayName != "alice@example.com" || seen.Workspace != "acme" || seen.Role != RoleEditor {
		t.Errorf("identity = %+v", seen)
	}
}

func TestRolePermissions(t *testing.T) {
	cases := []struct {
		role        Role
		read, write bool
	}{
		{RoleOwner, true, true},
		{RoleEditor, true, true},
		{RoleViewer, true, false},
		{"", false, false},
		{"superuser", false, false}, // unknown roles get nothing
	}
	for _, tc := range cases {
		id := Identity{Role: tc.role}
		if id.CanRead() != tc.read || id.CanWrite() != tc.write {
			t.Errorf("role %q: read=%v write=%v, want %v/%v", tc.role, id.CanRead(), id.CanWrite(), tc.read, tc.write)
		}
	}
}

func TestRequire(t *testing.T) {
	h := Require(Identity.CanWrite, "editors only")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	for role, want := range map[Role]int{RoleOwner: 204, RoleEditor: 204, RoleViewer: 403, "": 403} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(WithIdentity(req.Context(), Identity{Role: role}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("role %q: status %d, want %d", role, rec.Code, want)
		}
	}

	// No identity at all (Middleware was skipped) must fail closed.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing identity: status %d, want 401", rec.Code)
	}
}

// ADR 0041: a valid token cannot be upgraded by a forged X-Booth-Role header. This is the
// requirement booth-storage's first pass measured as a real gap; a catalog whose write
// routes trusted the header would let a viewer's token register and delete assets.
func TestMiddleware_RoleIsDerivedFromTheTokenNotTheHeader(t *testing.T) {
	verifier := stubVerifier{
		"viewer-token": {Subject: "carol", Groups: []string{"/workspaces/acme/viewer", "/workspaces/other/owner"}},
		"multi-token":  {Subject: "erin", Groups: []string{"/workspaces/acme/viewer", "/workspaces/acme/editor", "/workspaces/skip/owner", "not-a-workspace-group", "/workspaces/acme/superuser"}},
	}
	var got Identity
	h := Middleware(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, _ = FromContext(r.Context()) }))

	cases := []struct {
		name, token, workspace, header string
		wantStatus                     int
		want                           Role
	}{
		// A header stronger than the token grants is REJECTED, not downgraded.
		{"forged owner header with a viewer token", "viewer-token", "acme", "owner", 403, ""},
		{"forged editor header with a viewer token", "viewer-token", "acme", "editor", 403, ""},
		{"editor token claiming owner", "multi-token", "acme", "owner", 403, ""},
		{"absent header falls back to the token's grant", "viewer-token", "acme", "", 200, RoleViewer},
		{"a header may narrow the grant", "multi-token", "acme", "viewer", 200, RoleViewer},
		{"the highest of several grants applies in the workspace", "multi-token", "acme", "editor", 200, RoleEditor},
		{"a role held in ANOTHER workspace does not carry over", "viewer-token", "nowhere", "owner", 403, ""},
		{"an unrecognized forwarded role yields no access", "viewer-token", "acme", "root", 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got = Identity{}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			req.Header.Set(HeaderBoothWorkspace, tc.workspace)
			if tc.header != "" {
				req.Header.Set(HeaderBoothRole, tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantStatus == 200 && got.Role != tc.want {
				t.Errorf("effective role = %q, want %q", got.Role, tc.want)
			}
		})
	}
}

func TestEffectiveRole(t *testing.T) {
	cases := []struct{ forwarded, granted, want Role }{
		{"", RoleOwner, RoleOwner}, {"", RoleViewer, RoleViewer},
		{RoleOwner, RoleOwner, RoleOwner}, {RoleOwner, RoleViewer, RoleViewer}, {RoleEditor, RoleViewer, RoleViewer},
		{RoleViewer, RoleOwner, RoleViewer}, {RoleEditor, RoleOwner, RoleEditor},
		{"root", RoleOwner, ""}, {"OWNER", RoleOwner, ""},
	}
	for _, tc := range cases {
		if got := EffectiveRole(tc.forwarded, tc.granted); got != tc.want {
			t.Errorf("EffectiveRole(%q, %q) = %q, want %q", tc.forwarded, tc.granted, got, tc.want)
		}
	}
}

func TestRoleInWorkspace(t *testing.T) {
	groups := []string{"/workspaces/acme/viewer", "/workspaces/acme/owner", "/workspaces/acme-evil/owner"}
	if got := RoleInWorkspace(groups, "acme"); got != RoleOwner {
		t.Errorf("acme = %q, want owner", got)
	}
	// Similar-looking names and malformed groups must not match.
	if got := RoleInWorkspace([]string{"/workspaces/acme-evil/owner"}, "acme"); got != "" {
		t.Errorf("prefix-sibling workspace matched: %q", got)
	}
	bad := []string{"/workspaces/acme/owner/extra", "workspaces/acme/owner", "/workspaces/acme/superuser", "/workspaces/ACME/owner"}
	if got := RoleInWorkspace(bad, "acme"); got != "" {
		t.Errorf("malformed groups matched: %q", got)
	}
	if got := RoleInWorkspace(nil, "acme"); got != "" {
		t.Errorf("no groups matched: %q", got)
	}
}

// Through the real verifier: the groups claim is read from a genuinely signed token, under
// the configured claim name, and a malformed claim grants nothing.
func TestVerifier_ReadsGroupsClaim(t *testing.T) {
	idp := newFakeIdP(t)
	ctx := context.Background()

	def, err := NewVerifier(ctx, OIDCConfig{IssuerURL: idp.server.URL, ClientID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	custom, err := NewVerifier(ctx, OIDCConfig{IssuerURL: idp.server.URL, ClientID: "c", GroupsClaim: "memberships"})
	if err != nil {
		t.Fatal(err)
	}

	tok := idp.token(t, tokenOpts{subject: "alice", groups: []string{"/workspaces/acme/owner", "/workspaces/x/viewer"}})
	c, err := def.Verify(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Groups) != 2 || RoleInWorkspace(c.Groups, "acme") != RoleOwner {
		t.Errorf("groups = %v", c.Groups)
	}

	// Configured claim name: the default name is then ignored, the custom one read.
	if c, _ := custom.Verify(ctx, tok); len(c.Groups) != 0 {
		t.Errorf("custom-claim verifier read the default claim: %v", c.Groups)
	}
	tok2 := idp.token(t, tokenOpts{subject: "alice", groupsClaim: "memberships", groups: []string{"/workspaces/acme/editor"}})
	if c, _ := custom.Verify(ctx, tok2); RoleInWorkspace(c.Groups, "acme") != RoleEditor {
		t.Errorf("custom claim not read: %v", c.Groups)
	}

	// No claim, or one of the wrong shape: a genuine token that simply grants nothing.
	for name, groups := range map[string]any{"absent": nil, "a string": "/workspaces/acme/owner", "a number": 7, "an object": map[string]string{"a": "b"}} {
		c, err := def.Verify(ctx, idp.token(t, tokenOpts{subject: "alice", groups: groups}))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if RoleInWorkspace(c.Groups, "acme") != "" {
			t.Errorf("%s: granted a role from a malformed/absent claim: %v", name, c.Groups)
		}
	}
}
