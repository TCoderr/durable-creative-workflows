package platform

import "encoding/json"

// CapabilityRequest is the stateless HTTP contract. The Temporal activity's
// run identity stays in StepInput and PostgreSQL; it is not a capability input.
type CapabilityRequest struct {
	StepID         string                     `json:"step_id"`
	CommissionID   string                     `json:"commission_id"`
	Capability     string                     `json:"capability"`
	Brief          Brief                      `json:"brief"`
	Context        map[string]json.RawMessage `json:"context"`
	RevisionNumber int                        `json:"revision"`
}

func capabilityRequest(in StepInput) CapabilityRequest {
	brief := in.Brief
	// Omitted optional lists are valid in the public brief. Python's strict
	// contract expects arrays, so normalize the transport copy, not the record.
	if brief.Constraints == nil {
		brief.Constraints = []string{}
	}
	if brief.Prohibited == nil {
		brief.Prohibited = []string{}
	}
	if brief.BrandMemory == nil {
		brief.BrandMemory = []Memory{}
	}
	contextData := in.Context
	if contextData == nil {
		contextData = map[string]json.RawMessage{}
	}
	return CapabilityRequest{StepID: in.StepID, CommissionID: in.CommissionID, Capability: in.Capability,
		Brief: brief, Context: contextData, RevisionNumber: in.RevisionNumber}
}
