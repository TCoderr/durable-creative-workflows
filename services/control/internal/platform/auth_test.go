package platform

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestOIDCSignedJWKSBoundary(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, e)
	other, e := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, e)
	var fetches atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "jwks_uri": server.URL + "/jwks", "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			fetches.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()), "e": "AQAB"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, e := NewAuthenticator(context.Background(), Config{OIDCIssuer: server.URL, OIDCAudience: "velin-api"})
	require.NoError(t, e)
	for _, tt := range []struct {
		name, issuer, audience, sub     string
		expired, badSignature, wantPass bool
	}{{"valid", server.URL, "velin-api", "owner", false, false, true}, {"wrong issuer", server.URL + "/wrong", "velin-api", "owner", false, false, false}, {"wrong audience", server.URL, "another-api", "owner", false, false, false}, {"expired", server.URL, "velin-api", "owner", true, false, false}, {"empty subject", server.URL, "velin-api", "", false, false, false}, {"wrong signature", server.URL, "velin-api", "owner", false, true, false}} {
		t.Run(tt.name, func(t *testing.T) {
			expires := time.Now().Add(time.Minute).Unix()
			if tt.expired {
				expires = time.Now().Add(-time.Hour).Unix()
			}
			claims := jwt.MapClaims{"iss": tt.issuer, "aud": tt.audience, "sub": tt.sub, "exp": expires, "iat": time.Now().Add(-time.Hour).Unix()}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test-key"
			signer := key
			if tt.badSignature {
				signer = other
			}
			signed, e := token.SignedString(signer)
			require.NoError(t, e)
			req := httptest.NewRequest("GET", "/api/v1/session", nil)
			req.Header.Set("Authorization", "Bearer "+signed)
			subject, e := a.Subject(req)
			if tt.wantPass {
				require.NoError(t, e)
				require.Equal(t, "owner", subject)
			} else {
				require.Error(t, e)
				require.Empty(t, subject)
			}
		})
	}
	require.Greater(t, fetches.Load(), int32(0))
}
func TestRateLimitBurstAndCardinalityBound(t *testing.T) {
	l := NewRateLimiter()
	for i := 0; i < 120; i++ {
		require.True(t, l.Allow("owner"))
	}
	require.False(t, l.Allow("owner"))
	l = NewRateLimiter()
	for i := 0; i < 10000; i++ {
		require.True(t, l.Allow(fmt.Sprint(i)))
	}
	require.False(t, l.Allow("overflow"))
	require.Len(t, l.entries, 10000)
}
