package platform

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func sampleBrief() Brief {
	return Brief{Title: "Editorial commission", Objective: "Create an editorial identity with a clear reading hierarchy.", Audience: "Design readers", Constraints: []string{}, Prohibited: []string{}, BrandMemory: []Memory{}}
}

// fixture is the deterministic capability result used by every workflow test.
func fixture(in StepInput) StepResult {
	var out any = map[string]any{"summary": "bounded structured fixture"}
	switch in.Capability {
	case "research":
		out = map[string]any{"sources": []map[string]string{{"id": "source-1", "url": "https://example.org/source"}}}
	case "art_direction", "revision":
		out = Direction{Title: "Quiet edition", Objective: in.Brief.Objective, Audience: in.Brief.Audience, VisualPrinciples: []string{"hierarchy"}, Colors: []string{"paper"}, Typography: "serif", Motion: "subtle", Imagery: "still life", Composition: []string{"grid"}, Prohibitions: in.Brief.Prohibited, References: []string{"source-1"}, Uncertainties: []string{}}
		if in.Capability == "revision" {
			d := out.(Direction)
			d.Title = "Quiet edition, revision " + string(rune('0'+in.RevisionNumber))
			for _, capability := range []string{"typography", "motion", "imagery"} {
				var s struct {
					Direction string `json:"direction"`
				}
				if json.Unmarshal(in.Context[capability], &s) == nil && s.Direction != "" {
					switch capability {
					case "typography":
						d.Typography = s.Direction
					case "motion":
						d.Motion = s.Direction
					case "imagery":
						d.Imagery = s.Direction
					}
				}
			}
			out = d
		}
	case "typography", "motion", "imagery":
		out = map[string]string{"direction": "specialist " + in.Capability}
	case "critique":
		out = Critique{Score: 1, HardConstraintsPass: true, Summary: "Structural checks pass; subjective quality is unmeasured.", Issues: []string{}}
	}
	body, _ := json.Marshal(out)
	return StepResult{StepID: in.StepID, Capability: in.Capability, Output: body, Provenance: CapabilityProvenance{PromptID: in.Capability, PromptVersion: "1", PromptHash: strings.Repeat("a", 64), SchemaVersion: "1", Provider: "deterministic", Model: "fixture", Live: false}, Evaluations: []Evaluation{{Name: "structure", Mandatory: true, Passed: true}}, ToolCalls: []json.RawMessage{}, Invocations: []json.RawMessage{}}
}

// recorder is the in-memory activity set the workflow tests register. It keeps
// every recorded effect so tests can assert on what the workflow asked for.
type recorder struct {
	mu        sync.Mutex
	events    []EventInput
	revisions []Revision
	approvals []ApprovalInput
	decisions []DecisionInput
	produced  atomic.Int32
	decided   atomic.Int32
	capable   func(context.Context, StepInput, string) (StepResult, error)
}

func newRecorder(capable func(context.Context, StepInput, string) (StepResult, error)) *recorder {
	if capable == nil {
		capable = func(_ context.Context, in StepInput, _ string) (StepResult, error) { return fixture(in), nil }
	}
	return &recorder{capable: capable}
}
func (r *recorder) RecordRun(context.Context, RunInput) error { return nil }
func (r *recorder) RunCapability(ctx context.Context, in StepInput, parent string) (StepResult, error) {
	return r.capable(ctx, in, parent)
}
func (r *recorder) RecordEvent(_ context.Context, in EventInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, in)
	return nil
}
func (r *recorder) RecordRevision(_ context.Context, in RevisionInput) (Revision, error) {
	hash := HashJSON(in.Direction)
	rev := Revision{ID: RevisionID(in.CommissionID, in.Number, hash), CommissionID: in.CommissionID, Number: in.Number, ParentID: in.ParentID, DirectionHash: hash, Direction: in.Direction, Critique: in.Critique, ProducedBy: in.ProducedBy}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revisions = append(r.revisions, rev)
	return rev, nil
}
func (r *recorder) RecordApprovalRequest(_ context.Context, in ApprovalInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvals = append(r.approvals, in)
	return nil
}
func (r *recorder) RecordDecision(_ context.Context, in DecisionInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, in)
	r.decided.Add(1)
	return nil
}
func (r *recorder) ProduceArtifact(_ context.Context, in ProductionInput) (string, error) {
	r.produced.Add(1)
	return ArtifactID(in.Commission.ID), nil
}
func (r *recorder) eventTypes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	types := []string{}
	for _, e := range r.events {
		types = append(types, e.Type)
	}
	return types
}
func (r *recorder) terminal() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Kind == "workflow" && e.Type != "workflow.stage" {
			code, _ := e.Detail["code"].(string)
			return e.Type, code
		}
	}
	return "", ""
}

func registerRecorder(env *testsuite.TestWorkflowEnvironment, r *recorder) {
	env.RegisterWorkflowWithOptions(DurableCommission, workflowRegisterOptions())
	env.RegisterActivityWithOptions(r.RecordRun, activity.RegisterOptions{Name: RunActivity})
	env.RegisterActivityWithOptions(r.RunCapability, activity.RegisterOptions{Name: StepActivity})
	env.RegisterActivityWithOptions(r.RecordEvent, activity.RegisterOptions{Name: EventActivity})
	env.RegisterActivityWithOptions(r.RecordRevision, activity.RegisterOptions{Name: RevisionActivity})
	env.RegisterActivityWithOptions(r.RecordApprovalRequest, activity.RegisterOptions{Name: ApprovalActivity})
	env.RegisterActivityWithOptions(r.RecordDecision, activity.RegisterOptions{Name: DecisionActivity})
	env.RegisterActivityWithOptions(r.ProduceArtifact, activity.RegisterOptions{Name: ProductionActivity})
}

func setupWorkflow(t *testing.T, capable func(context.Context, StepInput, string) (StepResult, error)) (*testsuite.TestWorkflowEnvironment, *recorder) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	r := newRecorder(capable)
	registerRecorder(env, r)
	return env, r
}
func input() WorkflowInput {
	return WorkflowInput{Commission: Commission{ID: "3f1d5b3c-3a0f-4c1e-9d5e-1a2b3c4d5e6f", Brief: sampleBrief()}, OwnerID: "owner-1", ReviewTimeout: time.Hour}
}

// pending reads the approval the workflow is currently waiting on.
func pending(t *testing.T, env *testsuite.TestWorkflowEnvironment) ApprovalRequest {
	t.Helper()
	value, e := env.QueryWorkflow(StateQuery)
	require.NoError(t, e)
	var state WorkflowState
	require.NoError(t, value.Get(&state))
	require.NotNil(t, state.PendingApproval, "workflow is not waiting: stage %s", state.Stage)
	return *state.PendingApproval
}
func decisionFor(p ApprovalRequest, id, action string) Decision {
	return Decision{ID: id, ApprovalID: p.ID, RevisionID: p.RevisionID, Action: action, ReasonCode: "reviewed", Reason: "Reviewed the exact revision under approval", ActorID: "owner-1"}
}
