// Package config loads booth-catalog's runtime configuration from environment variables.
// Every value maps 1:1 to a Helm chart value/env var, mirroring booth-core's and
// booth-storage's own internal/config — there is no config file format of our own to version.
package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/projectbooth/booth-catalog/internal/auth"
	"github.com/projectbooth/booth-catalog/internal/code"
)

// Config is booth-catalog's full runtime configuration.
type Config struct {
	// HTTPAddr is the address the HTTP server listens on.
	HTTPAddr string

	// OIDC is the identity-provider configuration used to independently re-verify a forwarded
	// bearer token (core-platform-api.md's defense-in-depth requirement) and to re-derive the
	// caller's workspace role from it (ADR 0041).
	OIDC auth.OIDCConfig

	// PostgresDSN is the connection string for this module's own database on the shared
	// PostgreSQL cluster (ADR 0014).
	PostgresDSN string

	// NATSURL is the event bus (ADR 0021) the dashboard.* subscription connects to, e.g.
	// nats://booth-core-nats:4222. Empty disables the subscription: the data and code catalogs
	// work fully without it, dashboards simply never arrive. Chart installs always set it.
	NATSURL string

	// MaxCodeSourceBytes bounds one code version's source (default 1 MiB).
	MaxCodeSourceBytes int

	// DevMemory swaps PostgreSQL for in-process memory stores, so the module can run on a
	// laptop with no database. State vanishes on restart: it exists for local development only.
	DevMemory bool
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:           getEnv("BOOTH_HTTP_ADDR", ":8080"),
		PostgresDSN:        os.Getenv("BOOTH_POSTGRES_DSN"),
		NATSURL:            os.Getenv("BOOTH_NATS_URL"),
		MaxCodeSourceBytes: code.DefaultMaxSourceBytes,
		DevMemory:          os.Getenv("BOOTH_CATALOG_DEV_MEMORY") == "true",
		OIDC: auth.OIDCConfig{
			IssuerURL:       os.Getenv("BOOTH_OIDC_ISSUER_URL"),
			ClientID:        os.Getenv("BOOTH_OIDC_CLIENT_ID"),
			RequireAudience: os.Getenv("BOOTH_OIDC_REQUIRE_AUDIENCE") == "true",
			GroupsClaim:     getEnv("BOOTH_OIDC_GROUPS_CLAIM", auth.DefaultGroupsClaim),
		},
	}

	if v := os.Getenv("BOOTH_CATALOG_MAX_CODE_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("BOOTH_CATALOG_MAX_CODE_BYTES must be a positive integer, got %q", v)
		}
		cfg.MaxCodeSourceBytes = n
	}

	if cfg.OIDC.IssuerURL == "" {
		return Config{}, fmt.Errorf("BOOTH_OIDC_ISSUER_URL is required")
	}
	if cfg.OIDC.ClientID == "" {
		return Config{}, fmt.Errorf("BOOTH_OIDC_CLIENT_ID is required")
	}
	if !cfg.DevMemory && cfg.PostgresDSN == "" {
		return Config{}, fmt.Errorf("BOOTH_POSTGRES_DSN is required (set BOOTH_CATALOG_DEV_MEMORY=true only for local development)")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
