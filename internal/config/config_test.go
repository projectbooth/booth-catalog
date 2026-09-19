package config

import (
	"strings"
	"testing"
)

// setEnv sets exactly the given variables (clearing the ones Load reads) for one test.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"BOOTH_HTTP_ADDR", "BOOTH_POSTGRES_DSN", "BOOTH_NATS_URL", "BOOTH_CATALOG_MAX_CODE_BYTES", "BOOTH_CATALOG_DEV_MEMORY",
		"BOOTH_OIDC_ISSUER_URL", "BOOTH_OIDC_CLIENT_ID", "BOOTH_OIDC_REQUIRE_AUDIENCE", "BOOTH_OIDC_GROUPS_CLAIM",
	} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

var minimal = map[string]string{
	"BOOTH_OIDC_ISSUER_URL": "https://kc/realms/booth", "BOOTH_OIDC_CLIENT_ID": "booth-catalog", "BOOTH_POSTGRES_DSN": "postgres://x",
}

func with(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range minimal {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestLoad_Defaults(t *testing.T) {
	setEnv(t, minimal)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.MaxCodeSourceBytes != 1<<20 || cfg.NATSURL != "" || cfg.DevMemory || cfg.OIDC.RequireAudience {
		t.Errorf("defaults = %+v", cfg)
	}
	// The default must match booth-core's, or every request is refused (ADR 0041's fail-closed).
	if cfg.OIDC.GroupsClaim != "groups" {
		t.Errorf("GroupsClaim = %q, want booth-core's default", cfg.OIDC.GroupsClaim)
	}
}

func TestLoad_Overrides(t *testing.T) {
	setEnv(t, with(map[string]string{
		"BOOTH_HTTP_ADDR": ":9090", "BOOTH_NATS_URL": "nats://n:4222", "BOOTH_CATALOG_MAX_CODE_BYTES": "4096",
		"BOOTH_OIDC_REQUIRE_AUDIENCE": "true", "BOOTH_OIDC_GROUPS_CLAIM": "memberships",
	}))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.NATSURL != "nats://n:4222" || cfg.MaxCodeSourceBytes != 4096 || !cfg.OIDC.RequireAudience || cfg.OIDC.GroupsClaim != "memberships" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoad_Required(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"issuer":          {map[string]string{"BOOTH_OIDC_CLIENT_ID": "c", "BOOTH_POSTGRES_DSN": "x"}, "BOOTH_OIDC_ISSUER_URL"},
		"client id":       {map[string]string{"BOOTH_OIDC_ISSUER_URL": "i", "BOOTH_POSTGRES_DSN": "x"}, "BOOTH_OIDC_CLIENT_ID"},
		"database":        {map[string]string{"BOOTH_OIDC_ISSUER_URL": "i", "BOOTH_OIDC_CLIENT_ID": "c"}, "BOOTH_POSTGRES_DSN"},
		"bad code limit":  {with(map[string]string{"BOOTH_CATALOG_MAX_CODE_BYTES": "lots"}), "BOOTH_CATALOG_MAX_CODE_BYTES"},
		"zero code limit": {with(map[string]string{"BOOTH_CATALOG_MAX_CODE_BYTES": "0"}), "BOOTH_CATALOG_MAX_CODE_BYTES"},
	} {
		setEnv(t, tc.env)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to name %s", name, err, tc.want)
		}
	}
}

func TestLoad_DevMemoryWaivesTheDatabaseOnly(t *testing.T) {
	setEnv(t, map[string]string{"BOOTH_OIDC_ISSUER_URL": "i", "BOOTH_OIDC_CLIENT_ID": "c", "BOOTH_CATALOG_DEV_MEMORY": "true"})
	cfg, err := Load()
	if err != nil || !cfg.DevMemory {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
	// Authentication is never optional, even for local development.
	setEnv(t, map[string]string{"BOOTH_CATALOG_DEV_MEMORY": "true"})
	if _, err := Load(); err == nil {
		t.Error("dev mode dropped the OIDC requirement")
	}
}
