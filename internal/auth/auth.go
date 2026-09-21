// Package auth implements booth-catalog's side of the defense-in-depth requirement in
// contracts/core-platform-api.md: "a module must independently verify the identity core
// forwards it rather than trusting the network path implicitly."
//
// booth-core's gateway resolves workspace/role from the token's groups claim and forwards
// them as X-Booth-Workspace/X-Booth-Role (ADR 0025). This package does NOT take the
// forwarded role on trust (ADR 0041): verifying the token proves who the caller is, not
// that they hold the role in a header, and anyone able to reach the pod without going
// through the gateway could otherwise send a valid low-privilege token with a forged
// "owner" header. So the role is re-derived here from the verified token's groups claim,
// the effective role is never stronger than the token grants for the requested workspace,
// and a header claiming more than the token grants is rejected with 403.
//
// The shape deliberately mirrors booth-storage's internal/auth — same claim grammar, same
// rejection rule — so every module fails closed the same way.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// Header names the gateway forwards to a backing module (ADR 0025).
const (
	HeaderBoothWorkspace = "X-Booth-Workspace"
	HeaderBoothRole      = "X-Booth-Role"
)

// Role is a caller's workspace role (ADR 0025).
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Claims is the subset of a verified token this module cares about.
type Claims struct {
	Subject string
	// DisplayName is the most human-readable stable identifier the token offers
	// (preferred_username, then email, then the subject). It is what a new dataset's owner
	// defaults to — see docs/decisions/0003-owner-identity.md for why this is a plain string.
	DisplayName string
	// Groups is the token's workspace-membership claim (ADR 0025), e.g.
	// "/workspaces/acme/owner". Empty if the token carries none.
	Groups []string
}

// TokenVerifier verifies a raw bearer token. *Verifier is the production implementation;
// the interface exists so the HTTP layer can be tested without a live OIDC provider.
type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (*Claims, error)
}

// OIDCConfig is the identity-provider configuration — the same shape as booth-core's, so
// every module verifies against the same provider.
type OIDCConfig struct {
	IssuerURL       string
	ClientID        string
	RequireAudience bool
	// GroupsClaim names the token claim carrying workspace memberships. Configurable
	// because not every OIDC provider calls it "groups"; must match booth-core's setting.
	// Empty means the default, "groups".
	GroupsClaim string
}

// DefaultGroupsClaim matches booth-core's default (ADR 0025).
const DefaultGroupsClaim = "groups"

// Verifier verifies bearer tokens against the same OIDC provider booth-core is configured
// against (signature via JWKS, issuer, expiry, and, per deployment policy, audience).
type Verifier struct {
	idTokenVerifier *oidc.IDTokenVerifier
	groupsClaim     string
}

func NewVerifier(ctx context.Context, cfg OIDCConfig) (*Verifier, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery against %s: %w", cfg.IssuerURL, err)
	}
	return &Verifier{
		idTokenVerifier: provider.Verifier(&oidc.Config{
			SkipClientIDCheck: !cfg.RequireAudience,
			ClientID:          cfg.ClientID,
		}),
		groupsClaim: groupsClaimOrDefault(cfg.GroupsClaim),
	}, nil
}

// WorkloadJWKSPath is where booth-core publishes its workload-token verification keys,
// relative to its issuer URL (ADR 0056; booth-core's workload.JWKSPath).
const WorkloadJWKSPath = "/.well-known/jwks.json"

// NewWorkloadVerifier verifies the short-lived tokens booth-core mints for unattended runs
// (ADR 0056): a second trusted issuer alongside the deployment's OIDC provider. The token's
// groups claim has exactly the shape a human's does, so the same ADR 0041 role derivation
// applies unchanged; only the trust root differs.
//
// Unlike NewVerifier this does no discovery and touches no network at construction: core's
// JWKS location is fixed, and the keys are fetched (and cached) on first use. Startup must not
// depend on booth-core being reachable, and a core outage only affects workload tokens.
//
// cfg.IssuerURL is core's own issuer URL, which is not the OIDC provider's; ClientID,
// RequireAudience and GroupsClaim carry over from the human-token configuration because core
// mints `aud` and `groups` to match what modules already expect (ADR 0056).
func NewWorkloadVerifier(ctx context.Context, cfg OIDCConfig) *Verifier {
	issuer := strings.TrimRight(cfg.IssuerURL, "/")
	keys := oidc.NewRemoteKeySet(ctx, issuer+WorkloadJWKSPath)
	return &Verifier{
		idTokenVerifier: oidc.NewVerifier(issuer, keys, &oidc.Config{
			SkipClientIDCheck: !cfg.RequireAudience,
			ClientID:          cfg.ClientID,
		}),
		groupsClaim: groupsClaimOrDefault(cfg.GroupsClaim),
	}
}

func groupsClaimOrDefault(c string) string {
	if c == "" {
		return DefaultGroupsClaim
	}
	return c
}

// ChainVerifier accepts a token if any of its verifiers does. Each *Verifier checks the
// token's issuer against its own before anything else, so a token can only ever be accepted
// by the verifier configured for the issuer it names — there is no cross-issuer confusion to
// guard against, and the order only decides which error is reported.
type ChainVerifier []TokenVerifier

func (c ChainVerifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	var firstErr error
	for _, v := range c {
		claims, err := v.Verify(ctx, rawToken)
		if err == nil {
			return claims, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no token verifiers configured")
	}
	return nil, firstErr
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	idToken, err := v.idTokenVerifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("token verification failed: %w", err)
	}
	if idToken.Subject == "" {
		return nil, fmt.Errorf("token missing subject")
	}

	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return nil, fmt.Errorf("reading token claims: %w", err)
	}
	var groups []string
	if g, ok := raw[v.groupsClaim]; ok {
		// A claim of the wrong shape is treated as "no groups" (fail closed), not an error:
		// the token is genuine, it simply grants no workspace role.
		_ = json.Unmarshal(g, &groups)
	}

	display := idToken.Subject
	for _, name := range []string{"email", "preferred_username"} { // later wins: preferred_username beats email
		var s string
		if c, ok := raw[name]; ok && json.Unmarshal(c, &s) == nil && strings.TrimSpace(s) != "" {
			display = strings.TrimSpace(s)
		}
	}
	return &Claims{Subject: idToken.Subject, DisplayName: display, Groups: groups}, nil
}

// groupRE is ADR 0025's workspace-membership group shape.
var groupRE = regexp.MustCompile(`^/workspaces/([a-z0-9-]+)/(owner|editor|viewer)$`)

func rank(r Role) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// RoleInWorkspace returns the highest role the groups grant in workspace, or "" if none.
func RoleInWorkspace(groups []string, workspace string) Role {
	var best Role
	for _, g := range groups {
		m := groupRE.FindStringSubmatch(g)
		if m == nil || m[1] != workspace {
			continue
		}
		if r := Role(m[2]); rank(r) > rank(best) {
			best = r
		}
	}
	return best
}

// EffectiveRole combines the gateway-forwarded role with what the token itself grants: the
// result is never stronger than the token's grant, and never stronger than the forwarded
// role (a gateway may legitimately narrow, never widen). An absent forwarded role means
// "use the token's". Any unrecognized forwarded value yields "" — no access. (Middleware
// rejects a forwarded role that *exceeds* the grant before this is reached; this is the
// remaining narrowing/fallback logic.)
func EffectiveRole(forwarded, granted Role) Role {
	if forwarded == "" {
		return granted
	}
	if rank(forwarded) == 0 {
		return ""
	}
	if rank(forwarded) < rank(granted) {
		return forwarded
	}
	return granted
}

type contextKey struct{}

// Identity is the caller identity attached to a request's context by Middleware.
type Identity struct {
	Subject     string
	DisplayName string
	Workspace   string
	Role        Role
}

// CanRead reports whether the caller may browse and search the catalog: any recognized
// workspace role.
func (i Identity) CanRead() bool {
	return i.Role == RoleOwner || i.Role == RoleEditor || i.Role == RoleViewer
}

// CanWrite reports whether the caller may register, edit and remove datasets and code:
// editors and owners. Viewers are read-only. This mirrors booth-storage's ADR 0038 rule
// rather than inventing a catalog-specific permission model (see
// docs/decisions/0004-catalog-write-permissions.md).
func (i Identity) CanWrite() bool { return i.Role == RoleOwner || i.Role == RoleEditor }

// FromContext returns the identity Middleware attached, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// WithIdentity attaches an identity the way Middleware does — for tests exercising
// handlers downstream of it.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// Middleware verifies the request's bearer token and reads the gateway-forwarded
// workspace/role headers, attaching the result to the request context.
func Middleware(verifier TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				WriteError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := verifier.Verify(r.Context(), token)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			workspace := r.Header.Get(HeaderBoothWorkspace)
			if workspace == "" {
				WriteError(w, http.StatusBadRequest, "missing "+HeaderBoothWorkspace+" header")
				return
			}

			// Independent authorization: what does the *token* say this caller is in the
			// requested workspace? The forwarded header can only narrow that, never widen it.
			granted := RoleInWorkspace(claims.Groups, workspace)
			if granted == "" {
				WriteError(w, http.StatusForbidden, "your token grants no role in this workspace")
				return
			}
			forwarded := Role(r.Header.Get(HeaderBoothRole))
			// ADR 0041: a forwarded role stronger than the token grants is not "downgraded and
			// carried on" — it is a forged or corrupted request, and is rejected outright.
			if rank(forwarded) > rank(granted) {
				log.Printf("auth: rejected: forwarded role %q exceeds token-derived role %q for sub=%s workspace=%s", forwarded, granted, claims.Subject, workspace)
				WriteError(w, http.StatusForbidden, "the forwarded role exceeds what your token grants in this workspace")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), Identity{
				Subject:     claims.Subject,
				DisplayName: claims.DisplayName,
				Workspace:   workspace,
				Role:        EffectiveRole(forwarded, granted),
			})))
		})
	}
}

// Require returns middleware that rejects callers failing check with 403. It must run
// after Middleware.
func Require(check func(Identity) bool, message string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := FromContext(r.Context())
			if !ok {
				WriteError(w, http.StatusUnauthorized, "no identity")
				return
			}
			if !check(id) {
				WriteError(w, http.StatusForbidden, message)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteError writes the JSON error body used across this module's API.
func WriteError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
