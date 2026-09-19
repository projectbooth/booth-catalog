// Package contract validates booth-catalog's own manifest — the BoothModule custom resource
// its Helm chart templates — against contracts/module-manifest.md's schema, and checks the
// chart's security posture. Per contracts/testing-strategy.md this runs against a rendered
// template (via `helm template`), not a deployed cluster: no cluster needed, but a `helm`
// binary is, which CI's ci.yml sets up before running this alongside the rest of layers 1-2.
package contract

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

type boothModule struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Spec       struct {
		ID                string   `yaml:"id"`
		DisplayName       string   `yaml:"displayName"`
		Icon              string   `yaml:"icon"`
		Version           string   `yaml:"version"`
		ContractVersion   string   `yaml:"contractVersion"`
		HasOwnUI          bool     `yaml:"hasOwnUi"`
		UIIntegrationMode string   `yaml:"uiIntegrationMode"`
		HealthCheckPath   string   `yaml:"healthCheckPath"`
		RequiredScopes    []string `yaml:"requiredScopes"`
		NavGroup          string   `yaml:"navGroup"`
		NavPath           string   `yaml:"navPath"`
		AdminNavPath      string   `yaml:"adminNavPath"`
		ServiceRef        struct {
			Name string `yaml:"name"`
			Port int    `yaml:"port"`
		} `yaml:"serviceRef"`
	} `yaml:"spec"`
}

var requiredValues = []string{
	"--set", "oidc.issuerUrl=https://keycloak.example.com/realms/booth",
	"--set", "oidc.clientId=booth-catalog",
	"--set", "postgres.dsnSecret.name=booth-catalog-db",
}

func helm(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed; this contract test runs in CI where it is (see .github/workflows/ci.yml)")
	}
	return exec.Command("helm", args...).CombinedOutput()
}

func chartDir() string { return filepath.Join("..", "..", "charts", "booth-catalog") }

func helmTemplate(t *testing.T, showOnly string, extra ...string) []byte {
	t.Helper()
	args := []string{"template", "catalog-contract-test", chartDir(), "--namespace", "booth-catalog"}
	args = append(args, requiredValues...)
	args = append(args, extra...)
	if showOnly != "" {
		args = append(args, "--show-only", showOnly)
	}
	out, err := helm(t, args...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return out
}

func renderBoothModule(t *testing.T) boothModule {
	t.Helper()
	var m boothModule
	out := helmTemplate(t, "templates/boothmodule.yaml")
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatalf("parsing rendered BoothModule: %v\n%s", err, out)
	}
	return m
}

// TestManifest_RequiredFields checks every field contracts/module-manifest.md marks required.
func TestManifest_RequiredFields(t *testing.T) {
	m := renderBoothModule(t)
	if m.APIVersion != "booth.projectbooth.io/v1alpha1" || m.Kind != "BoothModule" {
		t.Errorf("apiVersion/kind = %s/%s, want booth.projectbooth.io/v1alpha1 BoothModule (ADR 0019)", m.APIVersion, m.Kind)
	}
	// The manifest contract: id "matches the repo name minus booth-".
	if m.Spec.ID != "catalog" {
		t.Errorf("spec.id = %q, want catalog", m.Spec.ID)
	}
	if m.Spec.DisplayName == "" {
		t.Error("spec.displayName is required but empty")
	}
	semver := regexp.MustCompile(`^\d+\.\d+\.\d+`)
	if !semver.MatchString(m.Spec.Version) || !semver.MatchString(m.Spec.ContractVersion) {
		t.Errorf("version=%q contractVersion=%q, want semver", m.Spec.Version, m.Spec.ContractVersion)
	}
	if m.Spec.HealthCheckPath == "" || m.Spec.HealthCheckPath[0] != '/' {
		t.Errorf("spec.healthCheckPath = %q, want a URL path", m.Spec.HealthCheckPath)
	}
	if m.Spec.ServiceRef.Name == "" || m.Spec.ServiceRef.Port == 0 {
		t.Errorf("spec.serviceRef is required (name+port) but got %+v", m.Spec.ServiceRef)
	}
}

// TestManifest_UI checks the "required if hasOwnUi" rules and the placement the brief and
// ADRs 0005/0017/0030 call for.
func TestManifest_UI(t *testing.T) {
	m := renderBoothModule(t)
	if !m.Spec.HasOwnUI {
		t.Fatal("spec.hasOwnUi = false, want true (the catalog ships a native UI)")
	}
	if m.Spec.UIIntegrationMode != "native" {
		t.Errorf("spec.uiIntegrationMode = %q, want native (ui-integration.md lists booth-catalog as a native-mode default)", m.Spec.UIIntegrationMode)
	}
	if m.Spec.NavGroup != "view" {
		t.Errorf("spec.navGroup = %q, want view (agent brief; ADR 0017)", m.Spec.NavGroup)
	}
	if m.Spec.NavPath != "/catalog" {
		t.Errorf("spec.navPath = %q, want /catalog (the UI package's default base path)", m.Spec.NavPath)
	}
	if m.Spec.AdminNavPath != "" {
		t.Errorf("spec.adminNavPath = %q; the catalog has no admin-only view (docs/decisions/0004)", m.Spec.AdminNavPath)
	}
}

// TestManifest_HealthPathMatchesProbe guards a subtle drift: the path core polls and the
// readiness probe hit the same real-readiness endpoint, and liveness deliberately doesn't.
func TestManifest_HealthPathMatchesProbe(t *testing.T) {
	m := renderBoothModule(t)
	dep := helmTemplate(t, "templates/deployment.yaml")
	if !regexp.MustCompile(`readinessProbe:\s+httpGet:\s+path: ` + regexp.QuoteMeta(m.Spec.HealthCheckPath) + `\b`).Match(dep) {
		t.Errorf("readinessProbe does not use the manifest's healthCheckPath %q:\n%s", m.Spec.HealthCheckPath, dep)
	}
	if !regexp.MustCompile(`livenessProbe:\s+httpGet:\s+path: /livez\b`).Match(dep) {
		t.Error("livenessProbe should use /livez: restarting the pod can't fix a database outage")
	}
}

// The module holds no Secrets and never calls the Kubernetes API, so it must not be granted
// RBAC or a mounted API token.
func TestChart_HasNoKubernetesAPIAccess(t *testing.T) {
	out := helmTemplate(t, "")
	for _, kind := range []string{"kind: Role", "kind: ClusterRole", "kind: RoleBinding", "kind: ClusterRoleBinding"} {
		if bytes.Contains(out, []byte(kind)) {
			t.Errorf("chart renders %q; booth-catalog needs no Kubernetes API access", kind)
		}
	}
	if !bytes.Contains(helmTemplate(t, "templates/deployment.yaml"), []byte("automountServiceAccountToken: false")) {
		t.Error("the pod mounts a service-account token it never uses")
	}
}

// TestChart_RequiresDatabaseSecret: rendering without a Postgres Secret must fail loudly
// rather than produce a Deployment that crash-loops at boot.
func TestChart_RequiresDatabaseSecret(t *testing.T) {
	out, err := helm(t, "template", "x", chartDir(), "--set", "oidc.issuerUrl=https://kc/realms/booth", "--set", "oidc.clientId=booth-catalog")
	if err == nil {
		t.Fatalf("chart rendered without postgres.dsnSecret.name:\n%s", out)
	}
	if !bytes.Contains(out, []byte("postgres.dsnSecret.name is required")) {
		t.Errorf("failure message not actionable:\n%s", out)
	}
}

// NATS has no safe default (its address depends on booth-core's release name), so the env var
// is rendered only when the operator sets it.
func TestChart_NATSURLIsOptInAndPassedThrough(t *testing.T) {
	if bytes.Contains(helmTemplate(t, "templates/deployment.yaml"), []byte("BOOTH_NATS_URL")) {
		t.Error("BOOTH_NATS_URL rendered with no nats.url configured")
	}
	with := helmTemplate(t, "templates/deployment.yaml", "--set", "nats.url=nats://core-nats:4222")
	if !regexp.MustCompile(`BOOTH_NATS_URL\s+value: "nats://core-nats:4222"`).Match(with) {
		t.Errorf("configured NATS URL not rendered:\n%s", with)
	}
}

// The module re-derives roles from the token's groups claim (ADR 0041), so the claim name must
// reach the pod and default to booth-core's.
func TestChart_PassesGroupsClaim(t *testing.T) {
	dep := helmTemplate(t, "templates/deployment.yaml")
	if !regexp.MustCompile(`BOOTH_OIDC_GROUPS_CLAIM\s+value: "groups"`).Match(dep) {
		t.Error("default groups claim should be \"groups\", matching booth-core")
	}
	if !bytes.Contains(helmTemplate(t, "templates/deployment.yaml", "--set", "oidc.groupsClaim=memberships"), []byte(`value: "memberships"`)) {
		t.Error("oidc.groupsClaim override not rendered")
	}
}
