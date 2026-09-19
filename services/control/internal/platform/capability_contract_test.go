package platform

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilityHTTPContractExcludesWorkflowEnvelopeAndNullCollections(t *testing.T) {
	in := StepInput{StepID: "commission:brief:0", CommissionID: "commission", RunID: "private-run", Capability: "brief",
		Brief: Brief{Title: "Editorial commission", Objective: "Create an editorial direction.", Audience: "Readers"}}
	data, err := json.Marshal(capabilityRequest(in))
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(data, &body))
	require.NotContains(t, body, "run_id", "the strict FastAPI schema rejects the Temporal-only field")
	require.NotContains(t, string(data), "null", "optional collections must cross the boundary as arrays or objects")
	require.Nil(t, in.Brief.Constraints, "transport normalization must not mutate immutable workflow input")
	require.Nil(t, in.Context)
	require.Equal(t, in.StepID, body["step_id"])
	require.Equal(t, in.CommissionID, body["commission_id"])
}
