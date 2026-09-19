package platform

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProductionDatabaseTLSUsesEffectiveConfig(t *testing.T) {
	for _, tt := range []struct {
		dsn   string
		valid bool
	}{{"postgresql://user@db.internal/velin?sslmode=verify-full", true}, {"host=db.internal user=velin dbname=velin sslmode=verify-full", true}, {"postgresql://user:sslmode%3Dverify-full@db.internal/velin?sslmode=disable", false}, {"postgresql://user@db.internal/velin?application_name=sslmode%3Dverify-full&sslmode=disable", false}, {"postgresql://user@db.internal/velin?sslmode=verify-full&sslmode=disable", false}, {"host=db.internal password='sslmode=verify-full' sslmode=disable", false}, {"postgresql://user@db.internal/velin?sslmode=verify-ca", false}, {"postgresql://user@db.internal/velin?sslmode=require", false}, {"postgresql://user@db.internal/velin?sslmode=prefer", false}} {
		require.Equal(t, tt.valid, verifiedDatabaseTLS(tt.dsn), tt.dsn)
	}
}
func TestProductionAuthFailsClosed(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgresql://user@db.internal/velin?sslmode=verify-full")
	t.Setenv("CAPABILITIES_TOKEN", strings.Repeat("a", 32))
	t.Setenv("OIDC_ISSUER", "https://identity.example.test")
	t.Setenv("OIDC_AUDIENCE", "velin-api")
	t.Setenv("TEMPORAL_TLS_CERT", "injected-cert.pem")
	t.Setenv("TEMPORAL_TLS_KEY", "injected-key.pem")
	t.Setenv("VELIN_API_KEYS", "")
	t.Setenv("REVIEW_TIMEOUT", "")
	cfg, e := LoadConfig()
	require.NoError(t, e)
	require.Equal(t, "velin-commissions", cfg.TaskQueue)
	require.Equal(t, 7*24*time.Hour, cfg.ReviewTimeout)
	t.Setenv("VELIN_API_KEYS", `{"`+strings.Repeat("b", 32)+`":"owner"}`)
	_, e = LoadConfig()
	require.Error(t, e)
}
func TestDevelopmentConfigAcceptsLocalTokensAndReviewTimeout(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgresql://velin@127.0.0.1/velin?sslmode=disable")
	t.Setenv("CAPABILITIES_TOKEN", strings.Repeat("c", 40))
	t.Setenv("VELIN_API_KEYS", `{"`+strings.Repeat("b", 32)+`":"owner"}`)
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("REVIEW_TIMEOUT", "90s")
	cfg, e := LoadConfig()
	require.NoError(t, e)
	require.Equal(t, 90*time.Second, cfg.ReviewTimeout)
	require.Equal(t, "owner", cfg.APIKeys[strings.Repeat("b", 32)])
	t.Setenv("REVIEW_TIMEOUT", "10ms")
	_, e = LoadConfig()
	require.Error(t, e)
	t.Setenv("REVIEW_TIMEOUT", "169h")
	_, e = LoadConfig()
	require.Error(t, e, "approval rounds must fit inside the execution deadline")
	t.Setenv("REVIEW_TIMEOUT", "")
	t.Setenv("CAPABILITIES_TOKEN", "short")
	_, e = LoadConfig()
	require.Error(t, e)
}
