package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBriefLimits(t *testing.T) {
	b := sampleBrief()
	require.NoError(t, b.Validate())
	b.Objective = strings.Repeat("x", 2001)
	require.Error(t, b.Validate())
	b = sampleBrief()
	b.Title = "日"
	require.Error(t, b.Validate())
	b = sampleBrief()
	b.Constraints = make([]string, 21)
	require.Error(t, b.Validate())
	b = sampleBrief()
	b.BrandMemory = []Memory{{ID: "invalid id", Source: "human", Value: "paper"}}
	require.Error(t, b.Validate())
}

func TestStepIdentityAndProvenance(t *testing.T) {
	in := StepInput{Capability: "art_direction", StepID: "run-1", Brief: sampleBrief()}
	r := fixture(in)
	require.NoError(t, ValidateStepResult(in, r))
	r.StepID = "other-step"
	require.Error(t, ValidateStepResult(in, r))
	r = fixture(in)
	r.Provenance.PromptHash = "missing"
	require.Error(t, ValidateStepResult(in, r))
	r = fixture(in)
	r.Output = json.RawMessage(`{"title":"incomplete"}`)
	require.Error(t, ValidateStepResult(in, r))
}

func gateFixtures() (Direction, []StepResult) {
	steps := []StepResult{}
	var d Direction
	for _, capability := range []string{"brief", "research", "strategy", "art_direction", "typography", "motion", "imagery", "critique"} {
		r := fixture(StepInput{Capability: capability, StepID: capability, Brief: sampleBrief()})
		steps = append(steps, r)
		if capability == "art_direction" {
			_ = json.Unmarshal(r.Output, &d)
		}
	}
	return d, steps
}
func TestProductionGate(t *testing.T) {
	d, steps := gateFixtures()
	c := Critique{HardConstraintsPass: true}
	require.NoError(t, ValidateProduction(d, sampleBrief(), c, steps))
	d.References = []string{"nonexistent-source"}
	require.Error(t, ValidateProduction(d, sampleBrief(), c, steps))
	d, steps = gateFixtures()
	steps[0].Evaluations = nil
	require.Error(t, ValidateProduction(d, sampleBrief(), c, steps))
	d, steps = gateFixtures()
	require.Error(t, ValidateProduction(d, sampleBrief(), Critique{}, steps))
	brief := sampleBrief()
	brief.Prohibited = []string{"neon gradients"}
	d, steps = gateFixtures()
	require.Error(t, ValidateProduction(d, brief, c, steps), "prohibition policy must carry forward")
	d.Prohibitions = []string{"neon gradients"}
	require.NoError(t, ValidateProduction(d, brief, c, steps))
	d.Imagery = "Neon gradients everywhere"
	require.Error(t, ValidateProduction(d, brief, c, steps))
}
func TestCorrectedRevisionDoesNotLoseFailedEvidence(t *testing.T) {
	d, steps := gateFixtures()
	steps[3].Evaluations[0].Passed = false
	require.Error(t, ValidateProduction(d, sampleBrief(), Critique{HardConstraintsPass: true}, steps))
	revision := fixture(StepInput{Capability: "revision", StepID: "revision-1", Brief: sampleBrief()})
	steps = append(steps, revision)
	require.NoError(t, ValidateProduction(d, sampleBrief(), Critique{HardConstraintsPass: true}, steps))
	require.False(t, steps[3].Evaluations[0].Passed)
}

func TestDecisionValidationAndBinding(t *testing.T) {
	p := ApprovalRequest{ID: ApprovalID("3f1d5b3c-3a0f-4c1e-9d5e-1a2b3c4d5e6f", 1), RevisionID: RevisionID("3f1d5b3c-3a0f-4c1e-9d5e-1a2b3c4d5e6f", 1, strings.Repeat("a", 64))}
	d := decisionFor(p, "decision-0001", ActionApprove)
	require.NoError(t, d.Validate())
	require.NoError(t, d.BindsTo(&p))
	other := p
	other.RevisionID = RevisionID(p.CommissionID, 2, strings.Repeat("b", 64))
	require.ErrorIs(t, d.BindsTo(&other), ErrStaleDecision)
	require.ErrorIs(t, d.BindsTo(nil), ErrStaleDecision)
	bad := d
	bad.ApprovalID = "not-a-uuid"
	require.Error(t, bad.Validate())
	bad = d
	bad.RevisionID = ""
	require.Error(t, bad.Validate())
	bad = d
	bad.ID = "short"
	require.Error(t, bad.Validate())
	bad = d
	bad.Action = "MAYBE"
	require.Error(t, bad.Validate())
	bad = decisionFor(p, "reject-0001", ActionReject)
	bad.Reason = ""
	require.Error(t, bad.Validate(), "rejections carry a reason")
	require.NoError(t, decisionFor(p, "reject-0002", ActionReject).Validate())
}

func TestDerivedIdentifiersAreStableAndContentBound(t *testing.T) {
	commission := "3f1d5b3c-3a0f-4c1e-9d5e-1a2b3c4d5e6f"
	require.Equal(t, RevisionID(commission, 1, "a"), RevisionID(commission, 1, "a"))
	require.NotEqual(t, RevisionID(commission, 1, "a"), RevisionID(commission, 1, "b"))
	require.NotEqual(t, RevisionID(commission, 1, "a"), RevisionID(commission, 2, "a"))
	require.Equal(t, ApprovalID(commission, 1), ApprovalID(commission, 1))
	require.NotEqual(t, ApprovalID(commission, 1), ApprovalID(commission, 2))
	require.Equal(t, RunID(commission, "temporal-run"), RunID(commission, "temporal-run"))
	require.NotEqual(t, RunID(commission, "run-a"), RunID(commission, "run-b"))
	require.Equal(t, StepID(commission, "critique", 2), commission+":critique:2")
}

func TestFileStoreContentAddressingAndTraversal(t *testing.T) {
	s, e := NewFileStore(t.TempDir())
	require.NoError(t, e)
	body := []byte(`{"safe":"artifact"}`)
	key, hash, e := s.Put(context.Background(), body)
	require.NoError(t, e)
	key2, hash2, e := s.Put(context.Background(), body)
	require.NoError(t, e)
	require.Equal(t, key, key2)
	require.Equal(t, hash, hash2)
	actual, e := s.Get(context.Background(), key)
	require.NoError(t, e)
	require.Equal(t, body, actual)
	_, e = s.Get(context.Background(), "../../secret")
	require.Error(t, e)
	require.NoError(t, os.WriteFile(filepath.Join(s.Root, key), []byte("poisoned"), 0600))
	_, e = s.Get(context.Background(), key)
	require.Error(t, e)
}

func TestAuthAndStrictJSON(t *testing.T) {
	a := Authenticator{Keys: map[string]string{strings.Repeat("x", 32): "owner-a"}}
	r := httptest.NewRequest("GET", "/", nil)
	_, e := a.Subject(r)
	require.Error(t, e)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	s, e := a.Subject(r)
	require.NoError(t, e)
	require.Equal(t, "owner-a", s)
	for _, body := range []string{`{"known":true,"unknown":true}`, `{"known":true}{"known":false}`, strings.Repeat("x", 32769)} {
		r = httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var in struct {
			Known bool `json:"known"`
		}
		require.False(t, decode(w, r, &in))
		require.True(t, w.Code == 400 || w.Code == 413)
	}
}
func TestPrivateAPIFailsClosedAndPublicRoutesValidateIdentifiers(t *testing.T) {
	api := &API{Auth: &Authenticator{Keys: map[string]string{}}, Limits: NewRateLimiter()}
	for _, path := range []string{"/api/v1/commissions", "/api/v1/workflows/a", "/api/v1/workflows/a/record", "/api/v1/artifacts/a", "/api/v1/provenance/a"} {
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 401, w.Code, path)
		require.NotContains(t, w.Body.String(), "stack")
	}
	w := httptest.NewRecorder()
	api.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/public/records/not-a-uuid", nil))
	require.Equal(t, 404, w.Code)
	w = httptest.NewRecorder()
	api.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}
func TestDecisionActorCannotBeSubmitted(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"decision_id":"approved-1","actor_id":"spoof"}`))
	r.Header.Set("Content-Type", "application/json")
	var in struct {
		ID string `json:"decision_id"`
	}
	require.False(t, decode(httptest.NewRecorder(), r, &in))
}
func TestOversizedMalformedBodyIs413(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 40000)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	var in any
	require.False(t, decode(w, r, &in))
	require.Equal(t, 413, w.Code)
}
func TestCapabilityContextProjectsOnlyDependencies(t *testing.T) {
	constraints := make([]string, 20)
	for i := range constraints {
		constraints[i] = strings.Repeat("x", 499)
	}
	body, _ := json.Marshal(map[string]any{"rules": constraints})
	direction, _ := json.Marshal(map[string]any{"visual_principles": constraints})
	all := map[string]json.RawMessage{"brief": body, "strategy": body, "research": json.RawMessage(`{"sources":[]}`), "art_direction": direction, "typography": body, "motion": body, "imagery": body, "revision": direction, "critique": json.RawMessage(`{}`)}
	encoded, _ := json.Marshal(all)
	require.Greater(t, len(encoded), 65536)
	selected, e := capabilityContext("critique", all)
	require.NoError(t, e)
	require.Len(t, selected, 2)
	require.Contains(t, selected, "revision")
	require.NotContains(t, selected, "art_direction")
	selected, e = capabilityContext("revision", all)
	require.NoError(t, e)
	encoded, _ = json.Marshal(selected)
	require.Less(t, len(encoded), 60<<10)
	all["research"] = json.RawMessage(`"` + strings.Repeat("x", 61<<10) + `"`)
	_, e = capabilityContext("critique", all)
	require.Error(t, e)
}
func TestTemporalMetricsBridgeRegistersOnceAcrossTagSets(t *testing.T) {
	handler := NewTemporalMetricsHandler()
	a := handler.WithTags(map[string]string{"namespace": "default", "workflow_type": WorkflowName})
	b := handler.WithTags(map[string]string{"namespace": "default", "failure_reason": "NonDeterminismError", "unknown_tag": "dropped"})
	a.Counter("temporal_workflow_task_execution_failed").Inc(1)
	b.Counter("temporal_workflow_task_execution_failed").Inc(2)
	a.Gauge("sticky_cache_size").Update(3)
	b.Timer("workflow_endtoend_latency").Record(1500)
	require.NotPanics(t, func() {
		handler.WithTags(map[string]string{"task_queue": "velin-commissions"}).Counter("temporal_workflow_task_execution_failed").Inc(1)
	})
	w := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, w.Body.String(), `temporal_workflow_task_execution_failed{`)
	require.Contains(t, w.Body.String(), `failure_reason="NonDeterminismError"`)
	require.Contains(t, w.Body.String(), "temporal_sticky_cache_size")
	require.Contains(t, w.Body.String(), "temporal_workflow_endtoend_latency_seconds")
	require.NotContains(t, w.Body.String(), "unknown_tag")
}
