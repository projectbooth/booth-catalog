// Package contract validates booth-catalog's own manifest — the BoothModule custom resource
// its Helm chart templates — against contracts/module-manifest.md's schema, and checks the
// chart's security posture. Per contracts/testing-strategy.md this runs against a rendered
// template (via `helm template`), not a deployed cluster: no cluster needed, but a `helm`
// binary is, which CI's ci.yml sets up before running this alongside the rest of layers 1-2.
package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
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
		Events            *struct {
			Publish   []string `yaml:"publish"`
			Subscribe []string `yaml:"subscribe"`
		} `yaml:"events"`
		Database *struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"database"`
		ServiceRef struct {
			Name string `yaml:"name"`
			Port int    `yaml:"port"`
		} `yaml:"serviceRef"`
	} `yaml:"spec"`
}

// The identity settings booth-core's install API supplies per ADR 0113's annotation mapping
// are the only values a render needs by default: postgres.provisionedByCore defaults to true,
// so no postgres.dsnSecret.name is required here (ADR 0053/0054, ARCHITECTURE.md item 68).
var requiredValues = []string{
	"--set", "oidc.issuerUrl=https://keycloak.example.com/realms/booth",
	"--set", "oidc.clientId=booth-catalog",
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

// TestManifest_DeclaresDatabase: by default booth-core provisions this module's database
// (ADR 0053/0054), so the manifest must ask for one and the Deployment must read exactly the
// Secret core delivers — otherwise the Module Store's Install button, which sends no values at
// all, has nowhere to get a connection string from (ARCHITECTURE.md item 68).
func TestManifest_DeclaresDatabase(t *testing.T) {
	m := renderBoothModule(t)
	if m.Spec.Database == nil || !m.Spec.Database.Enabled {
		t.Fatalf("spec.database = %+v, want {enabled: true} (module-manifest.md, ADR 0053)", m.Spec.Database)
	}

	dep := helmTemplate(t, "templates/deployment.yaml")
	if !regexp.MustCompile(`secretKeyRef:\s+name: booth-database-credentials\s+key: dsn`).Match(dep) {
		t.Errorf("BOOTH_POSTGRES_DSN is not read from booth-database-credentials/dsn:\n%s", dep)
	}
}

// TestChart_OwnDatabaseMode: an operator who brings their own database turns core's
// provisioning off. Then the manifest must NOT ask for one, and the Secret has to be named —
// rendering without it must fail loudly rather than produce a pod stuck waiting on a Secret
// nobody is going to create.
func TestChart_OwnDatabaseMode(t *testing.T) {
	out, err := helm(t, append(append([]string{"template", "x", chartDir()}, requiredValues...), "--set", "postgres.provisionedByCore=false")...)
	if err == nil {
		t.Fatalf("chart rendered with provisionedByCore=false and no Secret named:\n%s", out)
	}
	if !bytes.Contains(out, []byte("postgres.dsnSecret.name is required")) {
		t.Errorf("failure message not actionable:\n%s", out)
	}

	own := helmTemplate(t, "", "--set", "postgres.provisionedByCore=false", "--set", "postgres.dsnSecret.name=my-db", "--set", "postgres.dsnSecret.key=url")
	if !regexp.MustCompile(`secretKeyRef:\s+name: my-db\s+key: url`).Match(own) {
		t.Errorf("own-database Secret not used:\n%s", own)
	}
	var m boothModule
	if err := yaml.Unmarshal(helmTemplate(t, "templates/boothmodule.yaml", "--set", "postgres.provisionedByCore=false", "--set", "postgres.dsnSecret.name=my-db"), &m); err != nil {
		t.Fatal(err)
	}
	if m.Spec.Database != nil {
		t.Errorf("manifest still requests a core-provisioned database in own-database mode: %+v", m.Spec.Database)
	}
}

// ADR 0050: a module that omits `events` gets no event-bus credential and cannot connect to the
// bus at all. This is the field whose absence silently disabled the dashboard subscription
// (booth-e2e's SKIPped smoke.7-dashboard-event), so it is pinned exactly: the catalog subscribes
// to dashboard.*, table.* (ADR 0085) and database.table.* (ADR 0116) and — least privilege —
// publishes nothing.
func TestManifest_DeclaresItsEventBusUsage(t *testing.T) {
	m := renderBoothModule(t)
	if m.Spec.Events == nil {
		t.Fatal("spec.events is missing: without it booth-core mints no bus credential and the dashboard/table subscriptions cannot connect (ADR 0050)")
	}
	if want := []string{"dashboard.*", "table.*", "database.table.*"}; !reflect.DeepEqual(m.Spec.Events.Subscribe, want) {
		t.Errorf("spec.events.subscribe = %v, want exactly %v", m.Spec.Events.Subscribe, want)
	}
	if len(m.Spec.Events.Publish) != 0 {
		t.Errorf("spec.events.publish = %v; the catalog publishes no events and must not ask to", m.Spec.Events.Publish)
	}
	// The pattern must satisfy core's own grammar (its CRD validation): dotted lowercase tokens, a
	// literal first token, `*` only after it.
	pat := regexp.MustCompile(`^[a-z][a-z0-9]*(\.([a-z][a-z0-9]*|\*))+$`)
	for _, s := range m.Spec.Events.Subscribe {
		if !pat.MatchString(s) {
			t.Errorf("%q would be rejected by booth-core's event-pattern validation", s)
		}
	}
}

// The credential core writes for the declared events reaches the pod: the Secret is mounted as a
// directory (so in-place renewal propagates — a subPath mount would never update), the app is
// pointed at the file, and the bus address comes from the same Secret. Nothing is optional: a pod
// with no credential must wait, not start looking healthy while indexing nothing.
func TestChart_MountsTheEventBusCredential(t *testing.T) {
	dep := string(helmTemplate(t, "templates/deployment.yaml"))
	for _, want := range []string{
		"BOOTH_NATS_CREDS_FILE",
		"/etc/booth/event-bus/nats.creds",
		"secretName: booth-event-bus-credentials",
		"mountPath: /etc/booth/event-bus",
		"readOnly: true",
	} {
		if !strings.Contains(dep, want) {
			t.Errorf("deployment lacks %q:\n%s", want, dep)
		}
	}
	if !regexp.MustCompile(`BOOTH_NATS_URL\s+valueFrom:\s+secretKeyRef:\s+name: booth-event-bus-credentials\s+key: url`).MatchString(dep) {
		t.Errorf("the bus URL should come from the credentials Secret's url key:\n%s", dep)
	}
	if strings.Contains(dep, "subPath:") {
		t.Error("the credentials Secret is mounted with subPath, which the kubelet never updates: a renewed credential would not arrive")
	}
	if strings.Contains(dep, "optional: true") {
		t.Error("the credentials must not be optional: the pod should wait for them, not run without")
	}
}

// nats.url overrides the URL in the Secret (booth-e2e sets it by hand), but never replaces the
// credential itself.
func TestChart_NATSURLOverridesTheSecretsAddress(t *testing.T) {
	dep := string(helmTemplate(t, "templates/deployment.yaml", "--set", "nats.url=nats://core-nats:4222"))
	if !regexp.MustCompile(`BOOTH_NATS_URL\s+value: "nats://core-nats:4222"`).MatchString(dep) {
		t.Errorf("nats.url not rendered:\n%s", dep)
	}
	if !strings.Contains(dep, "BOOTH_NATS_CREDS_FILE") {
		t.Error("overriding the URL dropped the credential")
	}
}

// The event bus is either wired or explicitly off; there is no state where forgetting a value
// quietly leaves the dashboard subscription dead (the second silent failure booth-e2e found).
func TestChart_EventBusIsWiredOrExplicitlyOff(t *testing.T) {
	// Explicitly off: nothing about the bus is rendered — no events, no credential, no URL.
	off := []string{"--set", "eventBus.enabled=false"}
	m := helmTemplate(t, "templates/boothmodule.yaml", off...)
	dep := helmTemplate(t, "templates/deployment.yaml", off...)
	if bytes.Contains(m, []byte("events:")) {
		t.Errorf("events declared with the event bus off:\n%s", m)
	}
	for _, unwanted := range []string{"BOOTH_NATS", "event-bus-credentials", "volumes:"} {
		if bytes.Contains(dep, []byte(unwanted)) {
			t.Errorf("%q rendered with the event bus off:\n%s", unwanted, dep)
		}
	}

	// Credentials Secret switched off with no URL to fall back on: refuse to render, and say why.
	out, err := helm(t, append([]string{"template", "x", chartDir()}, append(requiredValues, "--set", "eventBus.credentialsSecret.enabled=false")...)...)
	if err == nil {
		t.Fatalf("rendered a deployment with no credentials and no bus address:\n%s", out)
	}
	if !bytes.Contains(out, []byte("nats.url is required")) {
		t.Errorf("failure message not actionable:\n%s", out)
	}

	// ...but with a URL (an unauthenticated stand-in bus) it is a deliberate, valid configuration.
	stand := helmTemplate(t, "templates/deployment.yaml", "--set", "eventBus.credentialsSecret.enabled=false", "--set", "nats.url=nats://nats:4222")
	if bytes.Contains(stand, []byte("BOOTH_NATS_CREDS_FILE")) || bytes.Contains(stand, []byte("event-bus-credentials")) {
		t.Errorf("credential wiring rendered with the credentials Secret disabled:\n%s", stand)
	}
	if !bytes.Contains(stand, []byte(`"nats://nats:4222"`)) {
		t.Errorf("stand-in bus URL not rendered:\n%s", stand)
	}
}

// Trusting booth-core as a second issuer (ADR 0056) is opt-in: nothing is rendered unless the
// operator names core's issuer URL.
func TestChart_WorkloadIssuer(t *testing.T) {
	if bytes.Contains(helmTemplate(t, "templates/deployment.yaml"), []byte("BOOTH_WORKLOAD_ISSUER_URL")) {
		t.Error("workload issuer rendered without workloadIdentity.issuerUrl being set")
	}
	out := helmTemplate(t, "templates/deployment.yaml", "--set", "workloadIdentity.issuerUrl=http://booth-core.booth-system.svc:8080")
	if !regexp.MustCompile(`BOOTH_WORKLOAD_ISSUER_URL\s+value: "http://booth-core.booth-system.svc:8080"`).Match(out) {
		t.Errorf("workload issuer not rendered:\n%s", out)
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

// ADR 0108: the key-fetch override defaults to empty (today's discovery behaviour, unchanged)
// and passes through when an operator sets it.
func TestChart_PassesJWKSURL(t *testing.T) {
	dep := helmTemplate(t, "templates/deployment.yaml")
	if !regexp.MustCompile(`BOOTH_OIDC_JWKS_URL\s+value: ""`).Match(dep) {
		t.Error("default oidc.jwksUrl should be empty (ordinary discovery)")
	}
	const jwksURL = "http://booth-core-keycloak.booth-system.svc.cluster.local:8080/realms/booth/protocol/openid-connect/certs"
	if !bytes.Contains(helmTemplate(t, "templates/deployment.yaml", "--set", "oidc.jwksUrl="+jwksURL), []byte(`value: "`+jwksURL+`"`)) {
		t.Error("oidc.jwksUrl override not rendered")
	}
}

// valueAtPath walks a dotted values.yaml path (e.g. "oidc.jwksUrl") through a generic
// YAML tree and reports whether it resolves to a leaf (which is all core's merge needs:
// a default to be absent-or-present under, per ADR 0113 item 8).
func valueAtPath(root map[string]any, dotted string) (any, bool) {
	cur := any(root)
	for _, part := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// TestChart_IdentityKeysAnnotation is the ADR 0113 / module-manifest.md "Chart
// annotation: identity keys" contract check: the annotation must be valid JSON, and
// every path it names must actually exist (with a default) in values.yaml, or core's
// install-time merge (and a plain `helm template`/CLI install) would silently do nothing
// for a typo'd path.
func TestChart_IdentityKeysAnnotation(t *testing.T) {
	chartBytes, err := os.ReadFile(filepath.Join("..", "..", "charts", "booth-catalog", "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Annotations map[string]string `yaml:"annotations"`
	}
	if err := yaml.Unmarshal(chartBytes, &chart); err != nil {
		t.Fatalf("parsing Chart.yaml: %v", err)
	}
	raw, ok := chart.Annotations["booth.projectbooth.io/identity-keys"]
	if !ok {
		t.Fatal("Chart.yaml is missing the booth.projectbooth.io/identity-keys annotation (ADR 0113)")
	}

	var mapping map[string]string
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		t.Fatalf("booth.projectbooth.io/identity-keys is not valid JSON: %v\nvalue: %s", err, raw)
	}
	if len(mapping) == 0 {
		t.Fatal("booth.projectbooth.io/identity-keys decoded to an empty object")
	}

	knownKeys := map[string]bool{
		"issuerUrl": true, "clientId": true, "jwksUrl": true,
		"workloadIssuerUrl": true, "groupsClaim": true, "iframeIssuerUrl": true,
	}

	valuesBytes, err := os.ReadFile(filepath.Join("..", "..", "charts", "booth-catalog", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(valuesBytes, &values); err != nil {
		t.Fatalf("parsing values.yaml: %v", err)
	}

	for key, path := range mapping {
		if !knownKeys[key] {
			t.Errorf("identity-keys annotation names unknown shared setting %q (want one of issuerUrl, clientId, jwksUrl, workloadIssuerUrl, groupsClaim, iframeIssuerUrl)", key)
		}
		if _, ok := valueAtPath(values, path); !ok {
			t.Errorf("identity-keys annotation maps %q to values.yaml path %q, which has no default in values.yaml", key, path)
		}
	}

	for _, want := range []struct{ sharedKey, path string }{
		{"issuerUrl", "oidc.issuerUrl"},
		{"clientId", "oidc.clientId"},
		{"jwksUrl", "oidc.jwksUrl"},
		{"workloadIssuerUrl", "workloadIdentity.issuerUrl"},
		{"groupsClaim", "oidc.groupsClaim"},
	} {
		if got := mapping[want.sharedKey]; got != want.path {
			t.Errorf("identity-keys[%q] = %q, want %q", want.sharedKey, got, want.path)
		}
	}
}
