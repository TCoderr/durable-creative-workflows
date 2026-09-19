package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Activities hold every side effect the workflow delegates. Each one is safe to
// repeat: accepted rows are keyed by stable identifiers and inserted with
// ON CONFLICT DO NOTHING, so a retry after a crash observes the earlier effect.
type Activities struct {
	Repo              *Repository
	Store             ObjectStore
	NATS              *nats.Conn
	CapabilitiesURL   string
	CapabilitiesToken string
	HTTP              *http.Client
}

const tracerName = "velin/control"

func attempt(ctx context.Context, name string) {
	retry := "false"
	if activity.GetInfo(ctx).Attempt > 1 {
		retry = "true"
		ActivityRetries.WithLabelValues(name).Inc()
	}
	ActivityAttempts.WithLabelValues(name, retry).Inc()
}

// RecordRun writes the run row for this Temporal execution and the run.started
// event. A second execution of the same commission gets attempt 2, which is how
// a fresh run is distinguished from a resume inside one run.
func (a *Activities) RecordRun(ctx context.Context, in RunInput) error {
	attempt(ctx, RunActivity)
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, in.TraceParent), "workflow.run.started")
	defer span.End()
	run, created, e := a.Repo.SaveRun(ctx, in.CommissionID, in.TemporalRunID)
	if e != nil {
		return e
	}
	span.SetAttributes(attribute.String("velin.commission_id", in.CommissionID), attribute.String("velin.run_id", run.ID), attribute.Int("velin.run_attempt", run.Attempt))
	if created {
		WorkflowsStarted.Inc()
	}
	typ := "run.started"
	detail := map[string]any{"run_id": run.ID, "temporal_run_id": run.TemporalRunID, "attempt": run.Attempt}
	kind := "run"
	if run.Attempt > 1 {
		typ = "run.resumed"
		kind = "recovery"
		WorkflowsResumed.Inc()
	}
	if e = a.Repo.Event(ctx, EventInput{CommissionID: in.CommissionID, RunID: run.ID, EventID: in.CommissionID + ":run:" + run.ID + ":started", Type: typ, Kind: kind, Subject: fmt.Sprintf("Run %d", run.Attempt), Detail: detail}); e != nil {
		return e
	}
	a.publish(in.CommissionID, map[string]any{"event_id": in.CommissionID + ":run:" + run.ID + ":started", "type": typ})
	return nil
}

// RunCapability calls the capability service for one step. The step id is
// stable, so a retried attempt first checks for an accepted result and returns
// it instead of calling the service again.
func (a *Activities) RunCapability(ctx context.Context, in StepInput, parent string) (StepResult, error) {
	attempt(ctx, StepActivity)
	started := time.Now()
	defer func() { CapabilityDuration.WithLabelValues(in.Capability).Observe(time.Since(started).Seconds()) }()
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, parent), "capability.activity")
	defer span.End()
	span.SetAttributes(attribute.String("velin.commission_id", in.CommissionID), attribute.String("velin.capability", in.Capability), attribute.String("velin.step_id", in.StepID), attribute.Int("temporal.attempt", int(activity.GetInfo(ctx).Attempt)))
	if existing, e := a.Repo.Step(ctx, in.StepID); e == nil {
		span.SetAttributes(attribute.Bool("velin.step_reused", true))
		return existing.StepResult, nil
	} else if !errors.Is(e, ErrNotFound) {
		return StepResult{}, e
	}
	attemptNumber := int(activity.GetInfo(ctx).Attempt)
	if e := a.Repo.Event(ctx, EventInput{CommissionID: in.CommissionID, RunID: in.RunID, EventID: in.StepID + ":attempt:" + strconv.Itoa(attemptNumber), Type: "step.attempted", Kind: "step", Subject: stepSubject(in, attemptNumber), Detail: map[string]any{"step_id": in.StepID, "capability": in.Capability, "attempt": attemptNumber, "revision_number": in.RevisionNumber}}); e != nil {
		return StepResult{}, e
	}
	body, e := json.Marshal(capabilityRequest(in))
	if e != nil {
		return StepResult{}, temporal.NewNonRetryableApplicationError("invalid activity input", "VALIDATION", nil)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.CapabilitiesURL+"/v1/capabilities/run", bytes.NewReader(body))
	if e != nil {
		return StepResult{}, e
	}
	req.Header.Set("Authorization", "Bearer "+a.CapabilitiesToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", traceParent(ctx))
	// Heartbeat while the capability service works so a worker crash is
	// detected by heartbeat timeout instead of the full start-to-close budget.
	stopHeartbeat := heartbeat(ctx, 3*time.Second)
	resp, e := a.HTTP.Do(req)
	stopHeartbeat()
	if e != nil {
		span.RecordError(errors.New("capability service transport failure"))
		if auditError := a.recordFailure(ctx, in, 0, CapabilityFailure{Code: "CAPABILITY_TRANSPORT_UNAVAILABLE"}); auditError != nil {
			return StepResult{}, auditError
		}
		return StepResult{}, errors.New("capability service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		evidence := parseCapabilityFailure(resp.Body)
		if e := a.recordFailure(ctx, in, resp.StatusCode, evidence); e != nil {
			return StepResult{}, e
		}
		span.SetAttributes(attribute.String("velin.failure_code", evidence.Code))
		if resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return StepResult{}, fmt.Errorf("capability service transient failure: %s (HTTP %d)", evidence.Code, resp.StatusCode)
		}
		return StepResult{}, temporal.NewNonRetryableApplicationError("capability service rejected request or structured output", evidence.Code, nil)
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil {
		return StepResult{}, e
	}
	if len(data) > 2<<20 {
		return StepResult{}, temporal.NewNonRetryableApplicationError("capability output oversized", "VALIDATION", nil)
	}
	var result StepResult
	if e = json.Unmarshal(data, &result); e != nil {
		return result, temporal.NewNonRetryableApplicationError("capability output JSON invalid", "VALIDATION", nil)
	}
	if e = ValidateStepResult(in, result); e != nil {
		return result, temporal.NewNonRetryableApplicationError("capability output provenance or schema invalid", "PROVENANCE_INVALID", nil)
	}
	stored, e := a.Repo.SaveStep(ctx, in, result)
	if e != nil {
		return result, e
	}
	a.publish(in.CommissionID, map[string]any{"event_id": in.StepID + ":completed", "type": "step.completed", "capability": in.Capability})
	slog.InfoContext(ctx, "step accepted", "event", "step_completed", "commission_id", in.CommissionID, "step_id", in.StepID, "provider", stored.Provenance.Provider, "model", stored.Provenance.Model, "live", stored.Provenance.Live, "attempt", attemptNumber)
	return stored, nil
}

func stepSubject(in StepInput, attempt int) string {
	return fmt.Sprintf("Step %s, revision %d, attempt %d", in.Capability, in.RevisionNumber, attempt)
}

// heartbeat reports activity liveness every interval until the returned stop
// function is called.
func heartbeat(ctx context.Context, interval time.Duration) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}

// RecordEvent appends one progress event and publishes a disposable hint.
func (a *Activities) RecordEvent(ctx context.Context, in EventInput) error {
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, in.TraceParent), "workflow."+in.Type)
	defer span.End()
	span.SetAttributes(attribute.String("velin.commission_id", in.CommissionID), attribute.String("velin.event_id", in.EventID))
	attempt(ctx, EventActivity)
	if e := a.Repo.Event(ctx, in); e != nil {
		return e
	}
	if in.Type == "workflow.stage" {
		if stage, ok := in.Detail["stage"].(string); ok {
			WorkflowTransitions.WithLabelValues(stage).Inc()
		}
	}
	if in.Kind == "workflow" && in.Type != "workflow.stage" {
		outcome := "unknown"
		if stage, ok := in.Detail["stage"].(string); ok {
			outcome = lower(stage)
		}
		WorkflowOutcomes.WithLabelValues(outcome).Inc()
		if raw, ok := in.Detail["duration_ms"]; ok {
			if ms, ok := numeric(raw); ok {
				WorkflowDuration.WithLabelValues(outcome).Observe(ms / 1000)
			}
		}
	}
	a.publish(in.CommissionID, map[string]any{"event_id": in.EventID, "type": in.Type, "detail": in.Detail})
	slog.InfoContext(ctx, "event recorded", "event", in.Type, "commission_id", in.CommissionID, "event_id", in.EventID)
	return nil
}

// RecordRevision persists the reviewed snapshot with a content-derived id.
func (a *Activities) RecordRevision(ctx context.Context, in RevisionInput) (Revision, error) {
	attempt(ctx, RevisionActivity)
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, in.TraceParent), "workflow.revision.created")
	defer span.End()
	rev, e := a.Repo.SaveRevision(ctx, in)
	if e != nil {
		if errors.Is(e, ErrConflict) {
			return rev, temporal.NewNonRetryableApplicationError("revision number already bound to different content", "DOMAIN_CONFLICT", nil)
		}
		return rev, e
	}
	span.SetAttributes(attribute.String("velin.commission_id", in.CommissionID), attribute.String("velin.revision_id", rev.ID))
	a.publish(in.CommissionID, map[string]any{"event_id": in.CommissionID + ":revision:" + rev.ID, "type": "revision.created"})
	return rev, nil
}

// RecordApprovalRequest persists the durable request to judge one revision.
func (a *Activities) RecordApprovalRequest(ctx context.Context, in ApprovalInput) error {
	attempt(ctx, ApprovalActivity)
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, in.TraceParent), "workflow.approval.requested")
	defer span.End()
	request := ApprovalRequest{ID: ApprovalID(in.CommissionID, in.Round), CommissionID: in.CommissionID, RevisionID: in.RevisionID, Round: in.Round, RequestedAt: in.RequestedAt, DeadlineAt: in.DeadlineAt}
	if e := a.Repo.SaveApproval(ctx, in.RunID, request); e != nil {
		if errors.Is(e, ErrConflict) {
			return temporal.NewNonRetryableApplicationError("approval round already bound to a different revision", "DOMAIN_CONFLICT", nil)
		}
		return e
	}
	span.SetAttributes(attribute.String("velin.commission_id", in.CommissionID), attribute.String("velin.approval_id", request.ID), attribute.String("velin.revision_id", request.RevisionID))
	a.publish(in.CommissionID, map[string]any{"event_id": in.CommissionID + ":approval:" + request.ID, "type": "approval.requested"})
	return nil
}

// RecordDecision persists an accepted decision. The database enforces that the
// decision's revision is the one its approval request named.
func (a *Activities) RecordDecision(ctx context.Context, in DecisionInput) error {
	attempt(ctx, DecisionActivity)
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, in.TraceParent), "workflow.decision.recorded")
	defer span.End()
	if e := a.Repo.SaveDecision(ctx, Commission{ID: in.CommissionID, OwnerID: in.OwnerID}, in.RunID, in.Decision, in.WaitedMS); e != nil {
		if errors.Is(e, ErrConflict) || errors.Is(e, ErrStaleDecision) {
			return temporal.NewNonRetryableApplicationError("decision does not bind to the recorded approval", "DOMAIN_CONFLICT", nil)
		}
		return e
	}
	ApprovalWait.Observe(float64(in.WaitedMS) / 1000)
	span.SetAttributes(attribute.String("velin.commission_id", in.CommissionID), attribute.String("velin.decision_id", in.Decision.ID), attribute.String("velin.revision_id", in.Decision.RevisionID))
	a.publish(in.CommissionID, map[string]any{"event_id": in.CommissionID + ":decision:" + in.Decision.ID, "type": "decision.recorded"})
	return nil
}

// ProduceArtifact assembles the delivered artifact from accepted evidence only,
// stores it content-addressed and records the artifact and its provenance.
func (a *Activities) ProduceArtifact(ctx context.Context, in ProductionInput) (string, error) {
	in.Commission.OwnerID = in.OwnerID
	attempt(ctx, ProductionActivity)
	ctx, span := otel.Tracer(tracerName).Start(traceContext(ctx, in.TraceParent), "artifact.production")
	defer span.End()
	span.SetAttributes(attribute.String("velin.commission_id", in.Commission.ID), attribute.String("velin.revision_id", in.Revision.ID))
	steps, e := a.Repo.Steps(ctx, in.Commission.ID)
	if e != nil {
		return "", e
	}
	decisions, e := a.Repo.Decisions(ctx, in.Commission.ID)
	if e != nil {
		return "", e
	}
	revision, e := a.Repo.Revision(ctx, in.Revision.ID)
	if e != nil {
		return "", temporal.NewNonRetryableApplicationError("approved revision evidence is missing", "DOMAIN_CONFLICT", nil)
	}
	if revision.CommissionID != in.Commission.ID || revision.DirectionHash != HashJSON(in.Direction) {
		return "", temporal.NewNonRetryableApplicationError("approved revision does not match the direction", "DOMAIN_CONFLICT", nil)
	}
	var approved *DecisionRecord
	for i, d := range decisions {
		if d.ID == in.Decision.ID && d.Action == ActionApprove && d.ActorID == in.Commission.OwnerID && d.ApprovalID == in.Approval.ID && d.RevisionID == in.Revision.ID {
			approved = &decisions[i]
		}
	}
	if approved == nil {
		return "", temporal.NewNonRetryableApplicationError("human approval evidence absent for this revision", "DOMAIN_CONFLICT", nil)
	}
	selected := []StepResult{}
	var critique Critique
	byID := map[string]StepRecord{}
	for _, r := range steps {
		byID[r.StepID] = r
	}
	for _, id := range in.StepIDs {
		r, ok := byID[id]
		if !ok {
			return "", temporal.NewNonRetryableApplicationError("accepted step evidence is missing", "DOMAIN_CONFLICT", nil)
		}
		selected = append(selected, r.StepResult)
		if r.Capability == "critique" {
			_ = json.Unmarshal(r.Output, &critique)
		}
	}
	if e = ValidateProduction(in.Direction, in.Commission.Brief, critique, selected); e != nil {
		return "", temporal.NewNonRetryableApplicationError("production evaluation gate failed", "DOMAIN_CONFLICT", nil)
	}
	id := ArtifactID(in.Commission.ID)
	artifact := map[string]any{
		"schema_version": "2", "id": id, "commission": in.Commission, "revision": revision, "direction": in.Direction,
		"approval": in.Approval, "decision": *approved, "steps": selected, "decisions": decisions,
		"provenance": map[string]any{"workflow_id": in.Commission.ID, "run_id": in.RunID, "brief_hash": HashJSON(in.Commission.Brief), "step_ids": in.StepIDs, "revision_id": in.Revision.ID, "approval_id": in.Approval.ID, "decision_id": approved.ID, "policy": "deterministic structural checks; no subjective quality claim"},
	}
	data, e := json.Marshal(artifact)
	if e != nil {
		return "", e
	}
	key, hash, e := a.Store.Put(ctx, data)
	if e != nil {
		return "", e
	}
	if e = a.Repo.SaveArtifact(ctx, in.RunID, Artifact{ID: id, CommissionID: in.Commission.ID, RevisionID: in.Revision.ID, DecisionID: approved.ID, Hash: hash, StorageKey: key}, in.Approval.ID, in.StepIDs); e != nil {
		if errors.Is(e, ErrConflict) {
			return "", temporal.NewNonRetryableApplicationError("a different artifact already exists for this commission", "DOMAIN_CONFLICT", nil)
		}
		return "", e
	}
	Artifacts.Inc()
	a.publish(in.Commission.ID, map[string]any{"event_id": id + ":recorded", "type": "artifact.recorded", "artifact_id": id})
	return id, nil
}

// publish sends a disposable progress hint. Loss is tolerated: consumers query
// authoritative state and the append-only record.
func (a *Activities) publish(id string, data any) {
	if a.NATS == nil || !a.NATS.IsConnected() {
		NATSPublishFailures.Inc()
		return
	}
	body, _ := json.Marshal(data)
	if e := a.NATS.Publish(ProgressSubject(id), body); e != nil {
		NATSPublishFailures.Inc()
		return
	}
	EventsPublished.Inc()
}

// ProgressSubject is the NATS subject carrying hints for one commission.
func ProgressSubject(commissionID string) string { return "velin.progress." + commissionID }

func numeric(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case json.Number:
		f, e := v.Float64()
		return f, e == nil
	}
	return 0, false
}
