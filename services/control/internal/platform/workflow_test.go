package platform

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func workflowRegisterOptions() workflow.RegisterOptions {
	return workflow.RegisterOptions{Name: WorkflowName}
}

func TestWorkflowTerminalEvidenceSurvivesExtendedPersistenceFailure(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	var attempts atomic.Int32
	env.OnActivity(EventActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in EventInput) error {
		if in.Type == "workflow.completed" && attempts.Add(1) <= 5 {
			return errors.New("persistence temporarily unavailable")
		}
		return r.RecordEvent(ctx, in)
	})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(DecisionSignal, decisionFor(pending(t, env), "approve-persistence-recovery", ActionApprove))
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	require.EqualValues(t, 6, attempts.Load(), "terminal persistence must outlive the capability retry budget")
	typ, _ := r.terminal()
	require.Equal(t, "workflow.completed", typ)
	require.EqualValues(t, 1, r.produced.Load())
	snapshot := r.events[len(r.events)-1].Detail["snapshot"]
	encoded, e := json.Marshal(snapshot)
	require.NoError(t, e)
	var retained WorkflowState
	require.NoError(t, json.Unmarshal(encoded, &retained))
	require.True(t, retained.Terminal)
	require.Equal(t, StageCompleted, retained.Stage)
	require.Nil(t, retained.Direction)
	require.Nil(t, retained.Critique)
}

func TestWorkflowFailedRunRegistrationStillRecordsTerminalEvidence(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	env.OnActivity(RunActivity, mock.Anything, mock.Anything).Return(errors.New("persistence unavailable"))
	env.ExecuteWorkflow(DurableCommission, input())
	require.Error(t, env.GetWorkflowError())
	typ, _ := r.terminal()
	require.Equal(t, "workflow.failed", typ)
	require.Empty(t, r.events[len(r.events)-1].RunID, "an unpersisted run cannot be a foreign key")
}

func TestWorkflowApproveRejectsStaleSpoofedAndDuplicateDecisions(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	env.RegisterDelayedCallback(func() {
		p := pending(t, env)
		stale := decisionFor(p, "stale-approval-0001", ActionApprove)
		stale.ApprovalID = ApprovalID(p.CommissionID, 99)
		env.SignalWorkflow(DecisionSignal, stale)
		wrongRevision := decisionFor(p, "stale-revision-0001", ActionApprove)
		wrongRevision.RevisionID = RevisionID(p.CommissionID, 9, "0000")
		env.SignalWorkflow(DecisionSignal, wrongRevision)
		spoof := decisionFor(p, "spoof-0001", ActionApprove)
		spoof.ActorID = "other-owner"
		env.SignalWorkflow(DecisionSignal, spoof)
		valid := decisionFor(p, "approve-0001", ActionApprove)
		env.SignalWorkflow(DecisionSignal, valid)
		env.SignalWorkflow(DecisionSignal, valid)
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	var state WorkflowState
	require.NoError(t, env.GetWorkflowResult(&state))
	require.Equal(t, StageCompleted, state.Stage)
	require.True(t, state.Terminal)
	require.False(t, state.CanApprove)
	require.Equal(t, MovementDone, state.Movement)
	require.Equal(t, 3, state.StaleDecisions)
	require.EqualValues(t, 1, r.produced.Load())
	require.EqualValues(t, 1, r.decided.Load())
	require.Equal(t, ArtifactID(state.CommissionID), state.ArtifactID)
	require.Len(t, r.revisions, 1)
	require.Equal(t, r.revisions[0].ID, r.decisions[0].Decision.RevisionID)
	require.Equal(t, r.approvals[0].RevisionID, r.decisions[0].Decision.RevisionID)
	typ, code := r.terminal()
	require.Equal(t, "workflow.completed", typ)
	require.Empty(t, code)
}

func TestWorkflowReviseThenApproveBindsEachRoundToItsRevision(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	var first ApprovalRequest
	env.RegisterDelayedCallback(func() {
		first = pending(t, env)
		env.SignalWorkflow(DecisionSignal, decisionFor(first, "revise-0001", ActionRevise))
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		second := pending(t, env)
		require.NotEqual(t, first.ID, second.ID)
		require.NotEqual(t, first.RevisionID, second.RevisionID)
		// A decision that still names round one is stale after the revision.
		env.SignalWorkflow(DecisionSignal, decisionFor(first, "late-approval-0001", ActionApprove))
		env.SignalWorkflow(DecisionSignal, decisionFor(second, "approve-0002", ActionApprove))
	}, 2*time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	var state WorkflowState
	require.NoError(t, env.GetWorkflowResult(&state))
	require.Equal(t, StageCompleted, state.Stage)
	require.Equal(t, 1, state.StaleDecisions)
	require.Equal(t, 2, state.ReviewRound)
	require.Equal(t, 1, state.RevisionNumber)
	require.EqualValues(t, 2, r.decided.Load())
	require.EqualValues(t, 1, r.produced.Load())
	require.Len(t, r.revisions, 2)
	require.Equal(t, r.revisions[0].ID, r.revisions[1].ParentID)
	require.Equal(t, r.revisions[1].ID, r.decisions[1].Decision.RevisionID)
	require.Equal(t, "approve-0002", state.DecisionID)
}

func TestWorkflowReject(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(DecisionSignal, decisionFor(pending(t, env), "reject-0001", ActionReject))
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	var s WorkflowState
	require.NoError(t, env.GetWorkflowResult(&s))
	require.Equal(t, StageRejected, s.Stage)
	require.True(t, s.Terminal)
	require.Zero(t, r.produced.Load())
	typ, _ := r.terminal()
	require.Equal(t, "workflow.rejected", typ)
}

func TestWorkflowApprovalExpiresIntoExplicitTerminalState(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	var s WorkflowState
	require.NoError(t, env.GetWorkflowResult(&s))
	require.Equal(t, StageExpired, s.Stage)
	require.Equal(t, "APPROVAL_EXPIRED", s.LastError)
	require.Nil(t, s.PendingApproval)
	require.Zero(t, r.produced.Load())
	require.Contains(t, r.eventTypes(), "approval.expired")
}

func TestWorkflowCancellation(t *testing.T) {
	env, r := setupWorkflow(t, nil)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.Error(t, env.GetWorkflowError())
	require.True(t, temporal.IsCanceledError(env.GetWorkflowError()))
	require.Zero(t, r.produced.Load())
	typ, code := r.terminal()
	require.Equal(t, "workflow.cancelled", typ)
	require.Equal(t, "WORKFLOW_CANCELLED", code)
}

func TestWorkflowTransientActivityRetry(t *testing.T) {
	var calls atomic.Int32
	env, r := setupWorkflow(t, func(_ context.Context, in StepInput, _ string) (StepResult, error) {
		if in.Capability == "brief" && calls.Add(1) == 1 {
			return StepResult{}, errors.New("provider temporarily unavailable")
		}
		return fixture(in), nil
	})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(DecisionSignal, decisionFor(pending(t, env), "approve-retry", ActionApprove))
	}, 3*time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 1, r.produced.Load())
}

func TestWorkflowPermanentFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	env, r := setupWorkflow(t, func(context.Context, StepInput, string) (StepResult, error) {
		calls.Add(1)
		return StepResult{}, temporal.NewNonRetryableApplicationError("invalid output", "VALIDATION", nil)
	})
	env.ExecuteWorkflow(DurableCommission, input())
	require.Error(t, env.GetWorkflowError())
	require.EqualValues(t, 1, calls.Load())
	require.Zero(t, r.produced.Load())
	typ, code := r.terminal()
	require.Equal(t, "workflow.failed", typ)
	require.Equal(t, "VALIDATION", code)
}

func TestWorkflowBoundedCritiqueRevisionsAndGatedApproval(t *testing.T) {
	var revisions atomic.Int32
	env, r := setupWorkflow(t, func(_ context.Context, in StepInput, _ string) (StepResult, error) {
		res := fixture(in)
		if in.Capability == "revision" {
			revisions.Add(1)
		}
		if in.Capability == "critique" {
			res.Output, _ = json.Marshal(Critique{Score: 0, RequiresRevision: true, HardConstraintsPass: false, Summary: "conflicting constraints"})
		}
		return res, nil
	})
	env.RegisterDelayedCallback(func() {
		p := pending(t, env)
		env.SignalWorkflow(DecisionSignal, decisionFor(p, "gated-approval", ActionApprove))
		env.SignalWorkflow(DecisionSignal, decisionFor(p, "reject-gate", ActionReject))
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
	var s WorkflowState
	require.NoError(t, env.GetWorkflowResult(&s))
	require.Equal(t, StageRejected, s.Stage)
	require.EqualValues(t, MaxRevisions-1, revisions.Load())
	require.Len(t, r.revisions, MaxRevisions)
	require.False(t, s.CanApprove)
	require.Equal(t, 1, s.StaleDecisions)
	require.Zero(t, r.produced.Load())
}

func TestWorkflowStateReportsRunAndVersion(t *testing.T) {
	env, _ := setupWorkflow(t, nil)
	env.RegisterDelayedCallback(func() {
		value, e := env.QueryWorkflow(StateQuery)
		require.NoError(t, e)
		var state WorkflowState
		require.NoError(t, value.Get(&state))
		require.Equal(t, WorkflowVersion, state.Version)
		require.NotEmpty(t, state.RunID)
		require.Equal(t, StageWaiting, state.Stage)
		require.Equal(t, MovementReview, state.Movement)
		require.Equal(t, RequiredStepCount, state.StepsCompleted)
		env.SignalWorkflow(DecisionSignal, decisionFor(*state.PendingApproval, "approve-state", ActionApprove))
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, input())
	require.NoError(t, env.GetWorkflowError())
}

func TestOwnerSurvivesTemporalSerialization(t *testing.T) {
	original := input()
	original.Commission.OwnerID = "not-public"
	data, e := json.Marshal(original)
	require.NoError(t, e)
	var roundtrip WorkflowInput
	require.NoError(t, json.Unmarshal(data, &roundtrip))
	require.Equal(t, "owner-1", roundtrip.OwnerID)
	require.Empty(t, roundtrip.Commission.OwnerID)
	env, r := setupWorkflow(t, nil)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(DecisionSignal, decisionFor(pending(t, env), "roundtrip-approve", ActionApprove))
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, roundtrip)
	require.NoError(t, env.GetWorkflowError())
	require.EqualValues(t, 1, r.produced.Load())
}

func TestActivityInputsCarryTraceParentThroughTemporalSerialization(t *testing.T) {
	// Activity inputs cross Temporal's data converter. A trace parent dropped
	// there detaches every stage and terminal span from the commission trace
	// without failing anything else, so each input is round-tripped through the
	// default converter and the recorded events are checked end to end.
	const parent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	dc := converter.GetDefaultDataConverter()
	roundtrip := func(in any, out any) {
		t.Helper()
		payload, e := dc.ToPayload(in)
		require.NoError(t, e)
		require.NoError(t, dc.FromPayload(payload, out))
	}
	var event EventInput
	roundtrip(EventInput{EventID: "e", TraceParent: parent}, &event)
	require.Equal(t, parent, event.TraceParent)
	var run RunInput
	roundtrip(RunInput{TraceParent: parent}, &run)
	require.Equal(t, parent, run.TraceParent)
	var revision RevisionInput
	roundtrip(RevisionInput{TraceParent: parent}, &revision)
	require.Equal(t, parent, revision.TraceParent)
	var approval ApprovalInput
	roundtrip(ApprovalInput{TraceParent: parent}, &approval)
	require.Equal(t, parent, approval.TraceParent)
	var decision DecisionInput
	roundtrip(DecisionInput{TraceParent: parent}, &decision)
	require.Equal(t, parent, decision.TraceParent)
	var production ProductionInput
	roundtrip(ProductionInput{TraceParent: parent}, &production)
	require.Equal(t, parent, production.TraceParent)

	env, r := setupWorkflow(t, nil)
	in := input()
	in.TraceParent = parent
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(DecisionSignal, decisionFor(pending(t, env), "trace-approve", ActionApprove))
	}, time.Second)
	env.ExecuteWorkflow(DurableCommission, in)
	require.NoError(t, env.GetWorkflowError())
	require.NotEmpty(t, r.events)
	for _, recorded := range r.events {
		require.Equal(t, parent, recorded.TraceParent, recorded.Type)
	}
	typ, _ := r.terminal()
	require.Equal(t, "workflow.completed", typ)
	require.Equal(t, parent, r.approvals[0].TraceParent)
	require.Equal(t, parent, r.decisions[0].TraceParent)
}

func TestTransitionTableIsClosed(t *testing.T) {
	for from, targets := range transitions {
		require.False(t, IsTerminal(from), from)
		for _, to := range targets {
			require.NoError(t, ValidTransition(from, to))
		}
	}
	for _, terminal := range []string{StageCompleted, StageRejected, StageExpired, StageCancelled, StageFailed} {
		require.True(t, IsTerminal(terminal))
		require.Empty(t, transitions[terminal])
		require.Equal(t, MovementDone, Movement(terminal))
	}
	require.Error(t, ValidTransition(StageDraft, StageCompleted))
	require.Error(t, ValidTransition(StageWaiting, StageWaiting))
	require.Error(t, ValidTransition(StageProduction, StageWaiting))
	require.Error(t, ValidTransition(StageCompleted, StageDraft))
	require.Equal(t, MovementBrief, Movement(StageBrief))
	require.Equal(t, MovementExecute, Movement(StageRevision))
	require.Equal(t, MovementReview, Movement(StageWaiting))
	require.Equal(t, MovementApprove, Movement(StageApproved))
	require.Equal(t, MovementContinue, Movement(StageProduction))
}
