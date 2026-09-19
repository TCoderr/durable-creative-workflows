package platform

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	"go.temporal.io/sdk/activity"
)

type ToolEvidence struct {
	ID            string   `json:"id"`
	Tool          string   `json:"tool"`
	ArgumentsHash string   `json:"arguments_hash"`
	Timestamp     string   `json:"timestamp"`
	Outcome       string   `json:"outcome"`
	LatencyMS     float64  `json:"latency_ms"`
	SourceIDs     []string `json:"source_ids"`
}
type InvocationEvidence struct {
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	Attempt   int     `json:"attempt"`
	Repair    bool    `json:"repair"`
	Outcome   string  `json:"outcome"`
	LatencyMS float64 `json:"latency_ms"`
	Live      bool    `json:"live"`
}
type CapabilityFailure struct {
	Code        string               `json:"code"`
	ToolCalls   []ToolEvidence       `json:"tool_calls"`
	Invocations []InvocationEvidence `json:"invocations"`
}

var acceptedFailureCodes = map[string]bool{"UNAUTHORIZED": true, "VALIDATION_ERROR": true, "PAYLOAD_TOO_LARGE": true, "STRUCTURED_OUTPUT_INVALID": true, "EVALUATION_FAILED": true, "CAPABILITY_TIMEOUT": true, "PROVIDER_TIMEOUT": true, "PROVIDER_UNAVAILABLE": true, "PROVIDER_RATE_LIMIT": true, "PROVIDER_REJECTED": true, "PROVIDER_RESPONSE_TOO_LARGE": true, "PROVIDER_NETWORK": true, "PROVIDER_ENVELOPE_INVALID": true, "PROVIDER_CIRCUIT_OPEN": true, "TOOL_NOT_ALLOWED": true, "TOOL_ARGUMENTS_INVALID": true, "TOOL_UNAVAILABLE": true, "PROMPT_NOT_FOUND": true, "PROMPT_INTEGRITY_FAILED": true, "PROMPT_REGISTRY_INVALID": true, "CAPACITY_EXCEEDED": true}

// parseCapabilityFailure keeps only a known code and bounded typed execution
// metadata from a failed response. Provider messages, body text, prompts and
// secrets never cross into the record.
func parseCapabilityFailure(reader io.Reader) CapabilityFailure {
	safe := CapabilityFailure{Code: "CAPABILITY_FAILURE", ToolCalls: []ToolEvidence{}, Invocations: []InvocationEvidence{}}
	data, e := io.ReadAll(io.LimitReader(reader, 65537))
	if e != nil || len(data) > 65536 {
		return safe
	}
	var raw CapabilityFailure
	if json.Unmarshal(data, &raw) != nil {
		return safe
	}
	if acceptedFailureCodes[raw.Code] {
		safe.Code = raw.Code
	}
	for _, tool := range raw.ToolCalls {
		if len(safe.ToolCalls) >= 8 {
			break
		}
		if !safeIdentifier.MatchString(tool.ID) || !safeIdentifier.MatchString(tool.Tool) || len(tool.ArgumentsHash) != 64 || len(tool.Timestamp) > 64 || math.IsNaN(tool.LatencyMS) || tool.LatencyMS < 0 || tool.LatencyMS > 120000 || len(tool.SourceIDs) > 20 {
			continue
		}
		if tool.Outcome != "failed" && tool.Outcome != "blocked" && tool.Outcome != "success" {
			continue
		}
		valid := true
		for _, id := range tool.SourceIDs {
			if !safeIdentifier.MatchString(id) {
				valid = false
			}
		}
		if valid {
			safe.ToolCalls = append(safe.ToolCalls, tool)
		}
	}
	for _, inv := range raw.Invocations {
		if len(safe.Invocations) >= 8 {
			break
		}
		if !safeMetadata(inv.Provider) || !safeMetadata(inv.Model) || inv.Attempt < 1 || inv.Attempt > 8 || inv.LatencyMS < 0 || inv.LatencyMS > 120000 {
			continue
		}
		if inv.Outcome != "success" && !acceptedFailureCodes[inv.Outcome] {
			inv.Outcome = "CAPABILITY_FAILURE"
		}
		safe.Invocations = append(safe.Invocations, inv)
	}
	return safe
}
func safeMetadata(value string) bool {
	return len(value) > 0 && len(value) <= 300 && !strings.ContainsAny(value, "\r\n\t\x00")
}

// recordFailure appends a step.failed event with safe evidence only. It is a
// failure in the record, never an accepted step.
func (a *Activities) recordFailure(ctx context.Context, in StepInput, status int, evidence CapabilityFailure) error {
	attemptNumber := int(activity.GetInfo(ctx).Attempt)
	ActivityFailures.WithLabelValues(StepActivity, evidence.Code).Inc()
	return a.Repo.Event(ctx, EventInput{CommissionID: in.CommissionID, RunID: in.RunID, EventID: in.StepID + ":attempt:" + strconv.Itoa(attemptNumber) + ":failure", Type: "step.failed", Kind: "failure", Subject: stepSubject(in, attemptNumber), Detail: map[string]any{"step_id": in.StepID, "capability": in.Capability, "attempt": attemptNumber, "http_status": status, "code": evidence.Code, "tool_calls": evidence.ToolCalls, "invocations": evidence.Invocations}})
}
