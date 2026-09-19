package platform

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailureBoundaryPreservesSafeEvidenceOnly(t *testing.T) {
	body := `{"code":"PROVIDER_TIMEOUT","message":"SECRET_VALUE never persist","traceId":"trace","tool_calls":[{"id":"tool:1","tool":"curated_research","arguments_hash":"` + strings.Repeat("a", 64) + `","timestamp":"2026-09-19T12:00:00Z","outcome":"failed","latency_ms":1,"source_ids":[]}],"invocations":[{"provider":"deterministic","model":"fixture","attempt":1,"repair":false,"outcome":"PROVIDER_TIMEOUT","latency_ms":1,"live":false}]}`
	safe := parseCapabilityFailure(strings.NewReader(body))
	require.Equal(t, "PROVIDER_TIMEOUT", safe.Code)
	require.Len(t, safe.ToolCalls, 1)
	require.Len(t, safe.Invocations, 1)
	encoded, e := json.Marshal(safe)
	require.NoError(t, e)
	require.NotContains(t, string(encoded), "SECRET_VALUE")
	require.NotContains(t, string(encoded), "message")
}
func TestFailureBoundaryRejectsRawOversizedMalformedMetadata(t *testing.T) {
	for _, body := range []string{`{"code":"SECRET_VALUE","message":"secret"}`, `{invalid`, strings.Repeat("x", 65537)} {
		safe := parseCapabilityFailure(strings.NewReader(body))
		require.Equal(t, "CAPABILITY_FAILURE", safe.Code)
	}
	safe := parseCapabilityFailure(strings.NewReader(`{"code":"TOOL_NOT_ALLOWED","tool_calls":[{"id":"raw secret\n","tool":"shell"}],"invocations":[{"provider":"bad\nmetadata","model":"secret","attempt":1}]}`))
	require.Empty(t, safe.ToolCalls)
	require.Empty(t, safe.Invocations)
}
