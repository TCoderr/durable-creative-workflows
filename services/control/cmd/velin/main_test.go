package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHealthOperatorEntrypoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	require.NoError(t, health(server.URL+"/health/ready"))
	for _, endpoint := range []string{"https://example.com/health/ready", "http://169.254.169.254/health/ready", server.URL + "/private", server.URL + "/health/ready?token=secret"} {
		require.Error(t, health(endpoint))
	}
	server.Close()
	require.Error(t, health(server.URL+"/health/ready"))
}
