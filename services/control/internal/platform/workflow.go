package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const WorkflowName = "DurableCommission"
const StateQuery = "state"
const DecisionSignal = "human-decision"
const DecisionReceiptQuery = "decision-receipt"

const (
	RunActivity        = "RecordRun"
	StepActivity       = "RunCapability"
	EventActivity      = "RecordEvent"
	RevisionActivity   = "RecordRevision"
	ApprovalActivity   = "RecordApprovalRequest"
	DecisionActivity   = "RecordDecision"
	ProductionActivity = "ProduceArtifact"
)

var pipeline = []struct{ capability, stage string }{
	{"brief", StageBrief}, {"research", StageResearch}, {"strategy", StageStrategy}, {"art_direction", StageDirection},
	{"typography", StageTypography}, {"motion", StageMotion}, {"imagery", StageImagery}, {"critique", StageCritique},
}

// DurableCommission is the sole owner of execution state. No database, wall
// clock, random source or model call belongs in this function. Every external
// effect has a stable identifier and is independently retry-safe, so a worker
// restart resumes from Temporal history without repeating accepted effects.
func DurableCommission(ctx workflow.Context, in WorkflowInput) (state WorkflowState, err error) {
	in.Commission.OwnerID = in.OwnerID
	info := workflow.GetInfo(ctx)
	runID := RunID(in.Commission.ID, info.WorkflowExecution.RunID)
	state = WorkflowState{CommissionID: in.Commission.ID, RunID: runID, Version: WorkflowVersion, Stage: StageDraft, Movement: MovementBrief}
	if err = workflow.SetQueryHandler(ctx, StateQuery, func() (WorkflowState, error) { return state, nil }); err != nil {
		return state, err
	}
	receipts := map[string]string{}
	if err = workflow.SetQueryHandler(ctx, DecisionReceiptQuery, func(id string) (string, error) { return receipts[id], nil }); err != nil {
		return state, err
	}
	// The heartbeat timeout is what lets Temporal notice a crashed worker
	// quickly: an abandoned attempt stops reporting and is retried elsewhere.
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 55 * time.Second, ScheduleToCloseTimeout: 4 * time.Minute, HeartbeatTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 15 * time.Second, MaximumAttempts: 3, NonRetryableErrorTypes: []string{"VALIDATION", "PERMANENT_PROVIDER", "PROVENANCE_INVALID", "DOMAIN_CONFLICT", "INVALID_TRANSITION"}},
	})
	startedAt := workflow.Now(ctx)
	runRecorded := false
	event := func(c workflow.Context, id, kind, typ, subject string, detail map[string]any) error {
		eventRunID := runID
		if id == "terminal" && !runRecorded && workflow.GetVersion(c, "terminal-without-run", workflow.DefaultVersion, 1) != workflow.DefaultVersion {
			eventRunID = ""
		}
		return workflow.ExecuteActivity(c, EventActivity, EventInput{CommissionID: in.Commission.ID, RunID: eventRunID, TraceParent: in.TraceParent, EventID: in.Commission.ID + ":" + id, Type: typ, Kind: kind, Subject: subject, Detail: detail}).Get(c, nil)
	}
	// Every ending is recorded as an explicit terminal event, on a disconnected
	// context so a cancelled workflow still records how it ended.
	defer func() {
		if err == nil && !IsTerminal(state.Stage) {
			err = temporal.NewNonRetryableApplicationError("workflow ended outside a terminal stage", "INVALID_TRANSITION", nil)
		}
		if err != nil {
			if temporal.IsCanceledError(err) {
				state.Stage = StageCancelled
				state.LastError = "WORKFLOW_CANCELLED"
			} else {
				state.Stage = StageFailed
				if state.LastError == "" {
					state.LastError = "ACTIVITY_FAILED"
					var applicationError *temporal.ApplicationError
					if errors.As(err, &applicationError) {
						state.LastError = applicationError.Type()
					}
				}
			}
		}
		state.Movement = MovementDone
		state.Terminal = false
		state.CanApprove = false
		state.PendingApproval = nil
		dctx, cancel := workflow.NewDisconnectedContext(ctx)
		defer cancel()
		version := workflow.GetVersion(dctx, "durable-terminal-record", workflow.DefaultVersion, 1)
		if version != workflow.DefaultVersion {
			// Keep the execution open until its terminal evidence is committed.
			// The execution deadline still bounds recovery; no write is silently lost.
			dctx = workflow.WithActivityOptions(dctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second,
				RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute}})
		}
		detail := map[string]any{"code": state.LastError, "stage": state.Stage, "outcome": lower(state.Stage), "duration_ms": workflow.Now(dctx).Sub(startedAt).Milliseconds(), "steps_completed": state.StepsCompleted, "revision_number": state.RevisionNumber, "review_round": state.ReviewRound}
		if workflow.GetVersion(dctx, "terminal-snapshot", workflow.DefaultVersion, 1) != workflow.DefaultVersion {
			// Retain the exact terminal observation after Temporal history expires.
			// Public progress records must never include the private direction body.
			snapshot := state
			snapshot.Terminal, snapshot.CanApprove = true, false
			snapshot.Direction, snapshot.Critique = nil, nil
			detail["snapshot"] = snapshot
		}
		e := event(dctx, "terminal", "workflow", "workflow."+lower(state.Stage), "Workflow "+in.Commission.ID, detail)
		if version != workflow.DefaultVersion && e != nil && err == nil {
			err = e
		}
		state.Terminal = e == nil || version == workflow.DefaultVersion
	}()
	transition := func(stage string) error {
		if e := ValidTransition(state.Stage, stage); e != nil {
			state.LastError = "INVALID_TRANSITION"
			return temporal.NewNonRetryableApplicationError(e.Error(), "INVALID_TRANSITION", nil)
		}
		previous := state.Stage
		state.Stage = stage
		state.Movement = Movement(stage)
		// A terminal stage becomes final only after its evidence is committed.
		state.Terminal = false
		return event(ctx, fmt.Sprintf("stage:%d:%d:%s", state.RevisionNumber, state.ReviewRound, stage), "workflow", "workflow.stage", stage, map[string]any{"from": previous, "stage": stage, "movement": state.Movement, "revision_number": state.RevisionNumber, "review_round": state.ReviewRound})
	}
	if err = workflow.ExecuteActivity(ctx, RunActivity, RunInput{CommissionID: in.Commission.ID, TemporalRunID: info.WorkflowExecution.RunID, TraceParent: in.TraceParent}).Get(ctx, nil); err != nil {
		return state, err
	}
	runRecorded = true
	contextData := map[string]json.RawMessage{}
	steps := []StepResult{}
	stepIDs := []string{}
	run := func(capability, stage string) error {
		if e := transition(stage); e != nil {
			return e
		}
		selectedContext, contextError := capabilityContext(capability, contextData)
		if contextError != nil {
			return temporal.NewNonRetryableApplicationError("capability context exceeds safe budget", "VALIDATION", nil)
		}
		step := StepInput{StepID: StepID(in.Commission.ID, capability, state.RevisionNumber), CommissionID: in.Commission.ID, RunID: runID, Capability: capability, Brief: in.Commission.Brief, Context: selectedContext, RevisionNumber: state.RevisionNumber}
		var result StepResult
		if e := workflow.ExecuteActivity(ctx, StepActivity, step, in.TraceParent).Get(ctx, &result); e != nil {
			return e
		}
		if e := ValidateStepResult(step, result); e != nil {
			return temporal.NewNonRetryableApplicationError("capability output rejected", "PROVENANCE_INVALID", e)
		}
		contextData[capability] = result.Output
		steps = append(steps, result)
		stepIDs = append(stepIDs, result.StepID)
		state.StepsCompleted++
		if capability == "art_direction" || capability == "revision" {
			var d Direction
			_ = json.Unmarshal(result.Output, &d)
			state.Direction = &d
		}
		if capability == "typography" || capability == "motion" || capability == "imagery" {
			var specialty struct {
				Direction string `json:"direction"`
			}
			_ = json.Unmarshal(result.Output, &specialty)
			if specialty.Direction == "" {
				return temporal.NewNonRetryableApplicationError("specialist direction empty", "VALIDATION", nil)
			}
			switch capability {
			case "typography":
				state.Direction.Typography = specialty.Direction
			case "motion":
				state.Direction.Motion = specialty.Direction
			case "imagery":
				state.Direction.Imagery = specialty.Direction
			}
			contextData["art_direction"], _ = json.Marshal(state.Direction)
		}
		if capability == "critique" {
			var c Critique
			_ = json.Unmarshal(result.Output, &c)
			state.Critique = &c
		}
		return nil
	}
	// A revision is the reviewed unit. It is recorded after every critique so the
	// approval that follows can name exact content.
	recordRevision := func() error {
		number := state.RevisionNumber + 1
		parent := ""
		if state.Revision != nil {
			parent = state.Revision.ID
		}
		producedBy := stepIDs[len(stepIDs)-1]
		for i := len(steps) - 1; i >= 0; i-- {
			if steps[i].Capability == "art_direction" || steps[i].Capability == "revision" {
				producedBy = steps[i].StepID
				break
			}
		}
		var rev Revision
		if e := workflow.ExecuteActivity(ctx, RevisionActivity, RevisionInput{CommissionID: in.Commission.ID, RunID: runID, Number: number, ParentID: parent, Direction: *state.Direction, Critique: *state.Critique, ProducedBy: producedBy, TraceParent: in.TraceParent}).Get(ctx, &rev); e != nil {
			return e
		}
		state.Revision = &RevisionRef{ID: rev.ID, Number: rev.Number, DirectionHash: rev.DirectionHash}
		return nil
	}
	for _, a := range pipeline {
		if err = run(a.capability, a.stage); err != nil {
			return state, err
		}
	}
	if err = recordRevision(); err != nil {
		return state, err
	}
	// Critique may ask for bounded revisions before a human sees anything.
	for state.Critique.RequiresRevision && state.RevisionNumber+1 < MaxRevisions {
		state.RevisionNumber++
		if err = run("revision", StageRevision); err != nil {
			return state, err
		}
		if err = run("critique", StageCritique); err != nil {
			return state, err
		}
		if err = recordRevision(); err != nil {
			return state, err
		}
	}
	decisions := workflow.GetSignalChannel(ctx, DecisionSignal)
	seen := map[string]bool{}
	for state.ReviewRound < MaxReviewRounds {
		state.ReviewRound++
		state.CanApprove = ValidateProduction(*state.Direction, in.Commission.Brief, *state.Critique, steps) == nil
		deadline := in.ReviewTimeout
		if deadline <= 0 {
			deadline = 7 * 24 * time.Hour
		}
		requestedAt := workflow.Now(ctx)
		pending := ApprovalRequest{ID: ApprovalID(in.Commission.ID, state.ReviewRound), CommissionID: in.Commission.ID, RevisionID: state.Revision.ID, Round: state.ReviewRound, RequestedAt: requestedAt, DeadlineAt: requestedAt.Add(deadline)}
		if err = workflow.ExecuteActivity(ctx, ApprovalActivity, ApprovalInput{CommissionID: in.Commission.ID, RunID: runID, RevisionID: pending.RevisionID, Round: pending.Round, RequestedAt: pending.RequestedAt, DeadlineAt: pending.DeadlineAt, TraceParent: in.TraceParent}).Get(ctx, nil); err != nil {
			return state, err
		}
		state.PendingApproval = &pending
		if err = transition(StageWaiting); err != nil {
			return state, err
		}
		timerCtx, cancelTimer := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, deadline)
		var accepted *Decision
		expired := false
		for accepted == nil && !expired {
			selector := workflow.NewSelector(ctx)
			selector.AddReceive(decisions, func(ch workflow.ReceiveChannel, more bool) {
				var d Decision
				ch.Receive(ctx, &d)
				if seen[d.ID] {
					return
				}
				seen[d.ID] = true
				// Every check is repeated here even though the API pre-validates:
				// the workflow is the last word on what a decision binds to.
				if d.Validate() != nil || d.ActorID != in.Commission.OwnerID || d.BindsTo(&pending) != nil || (d.Action == ActionApprove && !state.CanApprove) {
					state.StaleDecisions++
					return
				}
				accepted = &d
				receipts[d.ID] = HashJSON(d)
			})
			selector.AddFuture(timer, func(workflow.Future) { expired = true })
			selector.Select(ctx)
			if ctx.Err() != nil {
				cancelTimer()
				return state, ctx.Err()
			}
		}
		cancelTimer()
		waited := workflow.Now(ctx).Sub(requestedAt).Milliseconds()
		if expired {
			state.LastError = "APPROVAL_EXPIRED"
			state.PendingApproval = nil
			if err = event(ctx, "approval:"+pending.ID+":expired", "approval", "approval.expired", "Approval "+pending.ID, map[string]any{"approval_id": pending.ID, "revision_id": pending.RevisionID, "round": pending.Round, "waited_ms": waited}); err != nil {
				return state, err
			}
			err = transition(StageExpired)
			return state, err
		}
		state.DecisionID = accepted.ID
		state.PendingApproval = nil
		if err = workflow.ExecuteActivity(ctx, DecisionActivity, DecisionInput{CommissionID: in.Commission.ID, RunID: runID, OwnerID: in.OwnerID, Decision: *accepted, WaitedMS: waited, TraceParent: in.TraceParent}).Get(ctx, nil); err != nil {
			return state, err
		}
		switch accepted.Action {
		case ActionReject:
			err = transition(StageRejected)
			return state, err
		case ActionRevise:
			if state.RevisionNumber+1 >= MaxRevisions || state.ReviewRound >= MaxReviewRounds {
				state.LastError = "REVISION_LIMIT"
				return state, temporal.NewNonRetryableApplicationError("revision budget exhausted", "REVISION_LIMIT", nil)
			}
			state.RevisionNumber++
			contextData["human_decision"], _ = json.Marshal(accepted)
			if err = run("revision", StageRevision); err != nil {
				return state, err
			}
			if err = run("critique", StageCritique); err != nil {
				return state, err
			}
			if err = recordRevision(); err != nil {
				return state, err
			}
		case ActionApprove:
			if err = transition(StageApproved); err != nil {
				return state, err
			}
			if err = transition(StageProduction); err != nil {
				return state, err
			}
			production := ProductionInput{Commission: in.Commission, OwnerID: in.OwnerID, RunID: runID, Revision: *state.Revision, Direction: *state.Direction, Approval: pending, Decision: *accepted, StepIDs: stepIDs, TraceParent: in.TraceParent}
			if err = workflow.ExecuteActivity(ctx, ProductionActivity, production).Get(ctx, &state.ArtifactID); err != nil {
				return state, err
			}
			err = transition(StageCompleted)
			return state, err
		}
	}
	state.LastError = "REVIEW_LIMIT"
	return state, temporal.NewNonRetryableApplicationError("review limit reached", "REVISION_LIMIT", nil)
}

// capabilityContext selects only the dependencies of a capability. Historical
// step evidence stays in the record; it is never copied wholesale into
// successive model calls.
func capabilityContext(capability string, all map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	keys := []string{}
	candidate := "art_direction"
	if _, ok := all["revision"]; ok {
		candidate = "revision"
	}
	switch capability {
	case "strategy":
		keys = []string{"brief", "research"}
	case "art_direction":
		keys = []string{"brief", "research", "strategy"}
	case "typography", "motion", "imagery":
		keys = []string{candidate, "research", "strategy"}
	case "critique":
		keys = []string{candidate, "research"}
	case "revision":
		keys = []string{candidate, "research", "critique", "typography", "motion", "imagery", "human_decision"}
	}
	selected := map[string]json.RawMessage{}
	for _, key := range keys {
		if value, ok := all[key]; ok {
			selected[key] = value
		}
	}
	data, e := json.Marshal(selected)
	if e != nil {
		return nil, e
	}
	if len(data) > 60<<10 {
		return nil, fmt.Errorf("context is %d bytes", len(data))
	}
	return selected, nil
}

func lower(stage string) string {
	out := make([]byte, len(stage))
	for i := 0; i < len(stage); i++ {
		c := stage[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
