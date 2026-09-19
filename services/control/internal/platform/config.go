package platform

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Config struct {
	Environment       string
	HTTPAddr          string
	WorkerHTTPAddr    string
	DatabaseURL       string
	TemporalAddress   string
	TemporalNamespace string
	TaskQueue         string
	TemporalCert      string
	TemporalKey       string
	NATSURL           string
	CapabilitiesURL   string
	CapabilitiesToken string
	ArtifactRoot      string
	APIKeys           map[string]string
	OIDCIssuer        string
	OIDCAudience      string
	ProviderMode      string
	ReviewTimeout     time.Duration
}

func env(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

// LoadConfig reads the runtime configuration. Production fails closed: HTTPS
// OIDC only, no local tokens, verified PostgreSQL TLS and Temporal mTLS.
func LoadConfig() (Config, error) {
	c := Config{
		Environment: env("APP_ENV", "development"), HTTPAddr: env("HTTP_ADDR", ":8080"), WorkerHTTPAddr: env("WORKER_HTTP_ADDR", ":8081"),
		DatabaseURL: os.Getenv("DATABASE_URL"), TemporalAddress: env("TEMPORAL_ADDRESS", "127.0.0.1:7233"), TemporalNamespace: env("TEMPORAL_NAMESPACE", "default"),
		TaskQueue: env("TEMPORAL_TASK_QUEUE", "velin-commissions"), TemporalCert: os.Getenv("TEMPORAL_TLS_CERT"), TemporalKey: os.Getenv("TEMPORAL_TLS_KEY"),
		NATSURL: env("NATS_URL", "nats://127.0.0.1:4222"), CapabilitiesURL: env("CAPABILITIES_URL", "http://127.0.0.1:8000"), CapabilitiesToken: os.Getenv("CAPABILITIES_TOKEN"),
		ArtifactRoot: env("ARTIFACT_ROOT", ".runtime/artifacts"), OIDCIssuer: os.Getenv("OIDC_ISSUER"), OIDCAudience: os.Getenv("OIDC_AUDIENCE"),
		ProviderMode: env("VELIN_PROVIDER", "deterministic"), ReviewTimeout: 7 * 24 * time.Hour,
	}
	if raw := os.Getenv("REVIEW_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < time.Second || d > 7*24*time.Hour {
			return c, errors.New("REVIEW_TIMEOUT must be between one second and seven days")
		}
		c.ReviewTimeout = d
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if len(c.CapabilitiesToken) < 32 {
		return c, errors.New("CAPABILITIES_TOKEN must contain at least 32 characters")
	}
	if raw := os.Getenv("VELIN_API_KEYS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.APIKeys); err != nil {
			return c, errors.New("VELIN_API_KEYS must be a JSON object")
		}
		for k, v := range c.APIKeys {
			if len(k) < 24 || len(v) == 0 || len(v) > 256 {
				return c, errors.New("invalid local API token or subject")
			}
		}
	}
	if c.OIDCIssuer == "" && len(c.APIKeys) == 0 {
		return c, errors.New("OIDC or local API tokens must be configured")
	}
	if c.Environment == "production" {
		if c.OIDCIssuer == "" || c.OIDCAudience == "" || !strings.HasPrefix(c.OIDCIssuer, "https://") || len(c.APIKeys) > 0 {
			return c, errors.New("production requires HTTPS OIDC and forbids local API keys")
		}
		if c.TemporalCert == "" || c.TemporalKey == "" {
			return c, errors.New("production Temporal requires mTLS credentials")
		}
		if !verifiedDatabaseTLS(c.DatabaseURL) {
			return c, errors.New("production PostgreSQL requires sslmode=verify-full")
		}
	}
	return c, nil
}

// verifiedDatabaseTLS checks the connection settings pgx will actually use; a
// credential or unrelated query value containing 'sslmode=verify-full' cannot
// satisfy policy.
func verifiedDatabaseTLS(dsn string) bool {
	if u, e := url.Parse(dsn); e == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if len(u.Query()["sslmode"]) > 1 {
			return false
		}
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName == "" {
		return false
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify || fallback.TLSConfig.ServerName == "" {
			return false
		}
	}
	return true
}
