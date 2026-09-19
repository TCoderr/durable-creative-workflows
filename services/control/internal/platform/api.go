package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

type API struct {
	Config   Config
	Repo     *Repository
	Temporal client.Client
	NATS     *nats.Conn
	Store    ObjectStore
	Auth     *Authenticator
	Limits   *RateLimiter
}
type subjectKey struct{}

func subject(r *http.Request) string { s, _ := r.Context().Value(subjectKey{}).(string); return s }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message, "traceId": trace.SpanFromContext(r.Context()).SpanContext().TraceID().String()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength > 32768 {
		failure(w, r, 413, "PAYLOAD_TOO_LARGE", "Request exceeds 32 KiB.")
		return false
	}
	if media := strings.Split(r.Header.Get("Content-Type"), ";")[0]; media != "application/json" {
		failure(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		var large *http.MaxBytesError
		if errors.As(e, &large) {
			failure(w, r, 413, "PAYLOAD_TOO_LARGE", "Request exceeds 32 KiB.")
		} else {
			failure(w, r, 400, "VALIDATION_ERROR", "Request JSON is invalid.")
		}
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		failure(w, r, 400, "VALIDATION_ERROR", "Only one JSON object is allowed.")
		return false
	}
	return true
}

// Handler wires every route. Private routes require a verified subject and
// server-side ownership; public record routes are read-only and rate limited
// by client address.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /health/ready", a.ready)
	mux.HandleFunc("GET /api/v1/health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /api/v1/health/ready", a.ready)
	mux.Handle("GET /metrics", MetricsHandler())
	public := http.NewServeMux()
	public.HandleFunc("GET /api/v1/public/records", a.publicRecords)
	public.HandleFunc("GET /api/v1/public/records/{id}", a.publicRecord)
	mux.Handle("/api/v1/public/", a.publicLimit(public))
	private := http.NewServeMux()
	private.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"subject": subject(r), "mode": a.Auth.Mode, "provider_mode": a.Config.ProviderMode})
	})
	private.HandleFunc("POST /api/v1/commissions", a.create)
	private.HandleFunc("GET /api/v1/commissions", a.list)
	private.HandleFunc("GET /api/v1/commissions/{id}", a.get)
	private.HandleFunc("POST /api/v1/commissions/{id}/start", a.start)
	private.HandleFunc("POST /api/v1/commissions/{id}/publications", a.publish)
	private.HandleFunc("GET /api/v1/workflows/{id}", a.state)
	private.HandleFunc("GET /api/v1/workflows/{id}/record", a.record)
	private.HandleFunc("GET /api/v1/workflows/{id}/runs", a.runs)
	private.HandleFunc("GET /api/v1/workflows/{id}/approvals", a.approvals)
	private.HandleFunc("GET /api/v1/workflows/{id}/decisions", a.decisions)
	private.HandleFunc("GET /api/v1/workflows/{id}/timeline", a.timeline)
	private.HandleFunc("GET /api/v1/workflows/{id}/events", a.events)
	private.HandleFunc("POST /api/v1/workflows/{id}/decisions", a.decision)
	private.HandleFunc("POST /api/v1/workflows/{id}/cancel", a.cancel)
	private.HandleFunc("GET /api/v1/artifacts/{id}", a.artifact)
	private.HandleFunc("GET /api/v1/provenance/{id}", a.provenance)
	mux.Handle("/api/", a.authenticate(private))
	return a.telemetry(mux)
}
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, e := a.Auth.Subject(r)
		if e != nil {
			failure(w, r, 401, "UNAUTHORIZED", "Authentication required.")
			return
		}
		if !a.Limits.Allow(s) {
			w.Header().Set("Retry-After", "1")
			failure(w, r, 429, "RATE_LIMITED", "Request rate limit exceeded.")
			return
		}
		authenticated := r.WithContext(context.WithValue(r.Context(), subjectKey{}, s))
		next.ServeHTTP(w, authenticated)
		r.Pattern = authenticated.Pattern
	})
}
func (a *API) publicLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		if e != nil {
			host = r.RemoteAddr
		}
		if !a.Limits.Allow("public:" + host) {
			w.Header().Set("Retry-After", "1")
			failure(w, r, 429, "RATE_LIMITED", "Request rate limit exceeded.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseStatus) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (a *API) telemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer(tracerName).Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		r = r.WithContext(ctx)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Trace-Id", span.SpanContext().TraceID().String())
		wrapped := &responseStatus{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		span.SetAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", route), attribute.Int("http.response.status_code", wrapped.status))
		HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(wrapped.status/100)+"xx").Inc()
		if !strings.HasSuffix(r.URL.Path, "/events") {
			HTTPLatency.WithLabelValues(route).Observe(time.Since(started).Seconds())
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			slog.InfoContext(ctx, "HTTP request completed", "event", "http_request", "method", r.Method, "route", route, "status", wrapped.status, "trace_id", span.SpanContext().TraceID().String(), "duration_ms", time.Since(started).Milliseconds())
		}
	})
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	db := a.Repo != nil && a.Repo.Pool.Ping(ctx) == nil
	temporalOK := false
	if a.Temporal != nil {
		_, e := a.Temporal.CheckHealth(ctx, &client.CheckHealthRequest{})
		temporalOK = e == nil
	}
	status := 200
	if !db || !temporalOK {
		status = 503
	}
	writeJSON(w, status, map[string]any{"ready": status == 200, "postgresql": db, "temporal": temporalOK, "nats_optional": a.NATS != nil && a.NATS.IsConnected()})
}
func (a *API) owned(w http.ResponseWriter, r *http.Request) (Commission, bool) {
	id := r.PathValue("id")
	if _, e := uuid.Parse(id); e != nil {
		failure(w, r, 404, "NOT_FOUND", "Resource does not exist.")
		return Commission{}, false
	}
	c, e := a.Repo.Get(r.Context(), id, subject(r))
	if e != nil {
		a.repoError(w, r, e)
		return c, false
	}
	trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("velin.commission_id", c.ID))
	return c, true
}
func (a *API) repoError(w http.ResponseWriter, r *http.Request, e error) {
	if errors.Is(e, ErrNotFound) {
		failure(w, r, 404, "NOT_FOUND", "Resource does not exist.")
	} else if errors.Is(e, ErrConflict) {
		failure(w, r, 409, "IDEMPOTENCY_CONFLICT", "Idempotency key was already used with a different request.")
	} else {
		failure(w, r, 503, "DEPENDENCY_UNAVAILABLE", "A required dependency is unavailable.")
	}
}

/* ------------------------------------------------------------------ *
 * Commissions
 * ------------------------------------------------------------------ */

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		failure(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key must contain 8 to 128 bytes.")
		return
	}
	var in struct {
		Brief Brief `json:"brief"`
	}
	if !decode(w, r, &in) {
		return
	}
	if e := in.Brief.Validate(); e != nil {
		failure(w, r, 400, "VALIDATION_ERROR", e.Error())
		return
	}
	if in.Brief.Constraints == nil {
		in.Brief.Constraints = []string{}
	}
	if in.Brief.Prohibited == nil {
		in.Brief.Prohibited = []string{}
	}
	if in.Brief.BrandMemory == nil {
		in.Brief.BrandMemory = []Memory{}
	}
	c, created, e := a.Repo.Create(r.Context(), subject(r), key, in.Brief)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	status := 200
	if created {
		status = 201
	}
	w.Header().Set("Location", "/api/v1/commissions/"+c.ID)
	writeJSON(w, status, c)
}
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	limit := 50
	offset := 0
	var e error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 || limit > 100 {
			failure(w, r, 400, "VALIDATION_ERROR", "limit must be 1 to 100.")
			return
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, e = strconv.Atoi(raw)
		if e != nil || offset < 0 || offset > 10000 {
			failure(w, r, 400, "VALIDATION_ERROR", "offset must be 0 to 10000.")
			return
		}
	}
	items, e := a.Repo.List(r.Context(), subject(r), limit, offset)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset})
}
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	if c, ok := a.owned(w, r); ok {
		writeJSON(w, 200, map[string]any{"id": c.ID, "workflow_id": c.ID, "brief": c.Brief, "created_at": c.CreatedAt})
	}
}
func (a *API) start(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer(tracerName).Start(r.Context(), "workflow.start")
	defer span.End()
	r = r.WithContext(ctx)
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	runs, e := a.Repo.Runs(ctx, c.ID)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	if len(runs) > 0 {
		// Temporal's duplicate-ID protection ends with history retention. The
		// immutable run ledger keeps a finished commission from executing again.
		writeJSON(w, 202, map[string]string{"workflow_id": c.ID, "commission_id": c.ID})
		return
	}
	if _, snapshotError := a.Repo.TerminalSnapshot(ctx, c.ID); snapshotError == nil {
		writeJSON(w, 202, map[string]string{"workflow_id": c.ID, "commission_id": c.ID})
		return
	} else if !errors.Is(snapshotError, ErrNotFound) {
		a.repoError(w, r, snapshotError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, e = a.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: c.ID, TaskQueue: a.Config.TaskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionTimeout: 22 * 24 * time.Hour}, WorkflowName, WorkflowInput{Commission: c, OwnerID: c.OwnerID, TraceParent: traceParent(ctx), ReviewTimeout: a.Config.ReviewTimeout})
	var exists *serviceerror.WorkflowExecutionAlreadyStarted
	if e != nil && !errors.As(e, &exists) {
		failure(w, r, 503, "TEMPORAL_UNAVAILABLE", "Workflow could not be started; retry the same commission.")
		return
	}
	writeJSON(w, 202, map[string]string{"workflow_id": c.ID, "commission_id": c.ID})
}
func (a *API) publish(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	p, e := a.Repo.Publish(r.Context(), c.ID)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	_ = a.Repo.Event(r.Context(), EventInput{CommissionID: c.ID, EventID: c.ID + ":published", Type: "commission.published", Kind: "commission", Subject: "Commission " + c.ID, Detail: map[string]any{"publication_id": p.ID}})
	writeJSON(w, 201, map[string]any{"publication_id": p.ID, "commission_id": c.ID, "public_url": "/api/v1/public/records/" + p.ID})
}

/* ------------------------------------------------------------------ *
 * Workflow state and record
 * ------------------------------------------------------------------ */

func (a *API) query(ctx context.Context, id string) (WorkflowState, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var state WorkflowState
	value, e := a.Temporal.QueryWorkflow(ctx, id, "", StateQuery)
	if e != nil {
		return state, e
	}
	e = value.Get(&state)
	return state, e
}

// currentState returns the Temporal observation, or an immutable terminal
// snapshot after history retention. Only an unstarted commission is DRAFT.
func (a *API) currentState(ctx context.Context, c Commission) (WorkflowState, error) {
	if a.Temporal == nil {
		return WorkflowState{}, errors.New("temporal unavailable")
	}
	state, e := a.query(ctx, c.ID)
	if e != nil {
		var missing *serviceerror.NotFound
		if errors.As(e, &missing) {
			terminal, readError := a.Repo.TerminalSnapshot(ctx, c.ID)
			if readError == nil {
				return terminal, nil
			}
			if !errors.Is(readError, ErrNotFound) {
				return state, readError
			}
			runs, readError := a.Repo.Runs(ctx, c.ID)
			if readError != nil {
				return state, readError
			}
			if len(runs) > 0 {
				return state, e
			}
			return WorkflowState{CommissionID: c.ID, Version: WorkflowVersion, Stage: StageDraft, Movement: MovementBrief}, nil
		}
		return state, e
	}
	return state, nil
}
func (a *API) state(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	state, e := a.currentState(r.Context(), c)
	if e != nil {
		failure(w, r, 503, "WORKFLOW_QUERY_UNAVAILABLE", "Workflow state is temporarily unavailable.")
		return
	}
	writeJSON(w, 200, state)
}
func (a *API) record(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	rec, e := a.buildRecord(r.Context(), c, false)
	if e != nil {
		a.recordError(w, r, e)
		return
	}
	writeJSON(w, 200, rec)
}
func (a *API) recordError(w http.ResponseWriter, r *http.Request, e error) {
	if errors.Is(e, errWorkflowQuery) {
		failure(w, r, 503, "WORKFLOW_QUERY_UNAVAILABLE", "Workflow state is temporarily unavailable.")
		return
	}
	a.repoError(w, r, e)
}
func (a *API) runs(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	items, e := a.Repo.Runs(r.Context(), c.ID)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) approvals(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	views, e := a.approvalViews(r.Context(), c)
	if e != nil {
		a.recordError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": views})
}
func (a *API) decisions(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	items, e := a.Repo.Decisions(r.Context(), c.ID)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) timeline(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		var e error
		after, e = strconv.ParseInt(raw, 10, 64)
		if e != nil || after < 0 {
			failure(w, r, 400, "VALIDATION_ERROR", "after must be a non-negative sequence.")
			return
		}
	}
	items, e := a.Repo.Events(r.Context(), c.ID, after, 250)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

/* ------------------------------------------------------------------ *
 * Decisions and cancellation
 * ------------------------------------------------------------------ */

func (a *API) decision(w http.ResponseWriter, r *http.Request) {
	opCtx, span := otel.Tracer(tracerName).Start(r.Context(), "workflow.signal")
	defer span.End()
	r = r.WithContext(opCtx)
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	var in struct {
		ID         string `json:"decision_id"`
		ApprovalID string `json:"approval_id"`
		RevisionID string `json:"revision_id"`
		Action     string `json:"action"`
		ReasonCode string `json:"reason_code"`
		Reason     string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	d := Decision{ID: in.ID, ApprovalID: in.ApprovalID, RevisionID: in.RevisionID, Action: in.Action, ReasonCode: in.ReasonCode, Reason: in.Reason, ActorID: subject(r)}
	if e := d.Validate(); e != nil {
		failure(w, r, 400, "VALIDATION_ERROR", e.Error())
		return
	}
	prior, e := a.Repo.Decisions(r.Context(), c.ID)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	for _, p := range prior {
		if p.ID == d.ID {
			if HashJSON(p.Decision) != HashJSON(d) {
				failure(w, r, 409, "DECISION_CONFLICT", "Decision identifier was already used with different content.")
				return
			}
			writeJSON(w, 200, map[string]string{"decision_id": d.ID, "status": "accepted"})
			return
		}
	}
	state, e := a.query(r.Context(), c.ID)
	if e != nil {
		failure(w, r, 503, "TEMPORAL_UNAVAILABLE", "Workflow state unavailable.")
		return
	}
	if state.Stage != StageWaiting || state.PendingApproval == nil {
		// A signal may already be accepted in durable history while its decision
		// activity is still committing. Compare the workflow's receipt instead
		// of rejecting an exact retry in that interval.
		if state.DecisionID == d.ID {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			value, queryError := a.Temporal.QueryWorkflow(ctx, c.ID, "", DecisionReceiptQuery, d.ID)
			var fingerprint string
			if queryError == nil {
				queryError = value.Get(&fingerprint)
			}
			if queryError != nil || fingerprint == "" {
				failure(w, r, 503, "DECISION_RECEIPT_UNAVAILABLE", "Decision acknowledgement is temporarily unavailable; retry the same identifier.")
				return
			}
			if fingerprint != HashJSON(d) {
				failure(w, r, 409, "DECISION_CONFLICT", "Decision identifier was already used with different content.")
				return
			}
			writeJSON(w, 202, map[string]string{"decision_id": d.ID, "approval_id": d.ApprovalID, "revision_id": d.RevisionID, "status": "signal_received"})
			return
		}
		failure(w, r, 409, "NOT_WAITING", "Workflow is not waiting for a decision.")
		return
	}
	if e := d.BindsTo(state.PendingApproval); e != nil {
		writeJSON(w, 409, map[string]any{"code": "STALE_APPROVAL", "message": "Decision names a different approval or revision than the one under review.", "traceId": trace.SpanFromContext(r.Context()).SpanContext().TraceID().String(), "pending_approval": state.PendingApproval})
		return
	}
	if d.Action == ActionApprove && !state.CanApprove {
		failure(w, r, 409, "EVALUATION_GATE_FAILED", "Mandatory evaluations must pass before approval.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if e = a.Temporal.SignalWorkflow(ctx, c.ID, "", DecisionSignal, d); e != nil {
		failure(w, r, 503, "TEMPORAL_UNAVAILABLE", "Decision signal could not be sent; retry the same decision identifier.")
		return
	}
	writeJSON(w, 202, map[string]string{"decision_id": d.ID, "approval_id": d.ApprovalID, "revision_id": d.RevisionID, "status": "signal_sent"})
}
func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	if e := a.Repo.Event(r.Context(), EventInput{CommissionID: c.ID, EventID: c.ID + ":cancel-request", Type: "workflow.cancellation_requested", Kind: "workflow", Subject: "Workflow " + c.ID, Detail: map[string]any{"actor": "commission owner"}}); e != nil {
		a.repoError(w, r, e)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if e := a.Temporal.CancelWorkflow(ctx, c.ID, ""); e != nil {
		failure(w, r, 503, "TEMPORAL_UNAVAILABLE", "Cancellation could not be requested.")
		return
	}
	writeJSON(w, 202, map[string]string{"status": "cancellation_requested"})
}

/* ------------------------------------------------------------------ *
 * Artifacts and provenance
 * ------------------------------------------------------------------ */

func (a *API) artifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, e := uuid.Parse(id); e != nil {
		failure(w, r, 404, "NOT_FOUND", "Resource does not exist.")
		return
	}
	record, e := a.Repo.Artifact(r.Context(), id, subject(r))
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	data, e := a.Store.Get(r.Context(), record.StorageKey)
	if e != nil {
		failure(w, r, 503, "ARTIFACT_UNAVAILABLE", "Artifact storage is unavailable.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", `"`+record.Hash+`"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
func (a *API) provenance(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	lineage, e := a.lineage(r.Context(), c)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	writeJSON(w, 200, lineage)
}

// Lineage is the full provenance graph of a commission.
type Lineage struct {
	Commission          Commission         `json:"commission"`
	BriefHash           string             `json:"brief_hash"`
	Runs                []Run              `json:"runs"`
	Steps               []StepRecord       `json:"steps"`
	Revisions           []Revision         `json:"revisions"`
	Approvals           []ApprovalRequest  `json:"approvals"`
	Decisions           []DecisionRecord   `json:"decisions"`
	Artifacts           []Artifact         `json:"artifacts"`
	Provenance          []ProvenanceRecord `json:"provenance"`
	ChainOfThoughtStore bool               `json:"chain_of_thought_stored"`
}

func (a *API) lineage(ctx context.Context, c Commission) (Lineage, error) {
	l := Lineage{Commission: c, BriefHash: HashJSON(c.Brief)}
	var e error
	if l.Runs, e = a.Repo.Runs(ctx, c.ID); e != nil {
		return l, e
	}
	if l.Steps, e = a.Repo.Steps(ctx, c.ID); e != nil {
		return l, e
	}
	if l.Revisions, e = a.Repo.Revisions(ctx, c.ID); e != nil {
		return l, e
	}
	if l.Approvals, e = a.Repo.Approvals(ctx, c.ID); e != nil {
		return l, e
	}
	if l.Decisions, e = a.Repo.Decisions(ctx, c.ID); e != nil {
		return l, e
	}
	if l.Artifacts, e = a.Repo.Artifacts(ctx, c.ID); e != nil {
		return l, e
	}
	if l.Provenance, e = a.Repo.Provenance(ctx, c.ID); e != nil {
		return l, e
	}
	return l, nil
}

/* ------------------------------------------------------------------ *
 * Aggregate record
 * ------------------------------------------------------------------ */

var errWorkflowQuery = errors.New("workflow query unavailable")

// StepSummary is a step without its full output.
type StepSummary struct {
	StepID            string    `json:"step_id"`
	RunID             string    `json:"run_id"`
	Capability        string    `json:"capability"`
	RevisionNumber    int       `json:"revision_number"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Live              bool      `json:"live"`
	PromptID          string    `json:"prompt_id"`
	PromptVersion     string    `json:"prompt_version"`
	EvaluationsPassed bool      `json:"evaluations_passed"`
	CreatedAt         time.Time `json:"created_at"`
}

// RevisionSummary is a revision without the full direction text.
type RevisionSummary struct {
	ID                  string    `json:"id"`
	Number              int       `json:"number"`
	ParentID            string    `json:"parent_id,omitempty"`
	DirectionHash       string    `json:"direction_hash"`
	Title               string    `json:"title"`
	ProducedBy          string    `json:"produced_by"`
	RequiresRevision    bool      `json:"requires_revision"`
	HardConstraintsPass bool      `json:"hard_constraints_pass"`
	CreatedAt           time.Time `json:"created_at"`
}

// ApprovalView is an approval request with its derived status.
type ApprovalView struct {
	ApprovalRequest
	Status     string `json:"status"`
	DecisionID string `json:"decision_id,omitempty"`
	Action     string `json:"action,omitempty"`
}

// PublicCommission is what a published record reveals about its commission.
type PublicCommission struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Objective string    `json:"objective"`
	Audience  string    `json:"audience"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkflowRecord is the aggregate the frontend renders: authoritative Temporal
// state plus the append-only PostgreSQL record.
type WorkflowRecord struct {
	Commission PublicCommission   `json:"commission"`
	Workflow   WorkflowState      `json:"workflow"`
	Runs       []Run              `json:"runs"`
	Steps      []StepSummary      `json:"steps"`
	Revisions  []RevisionSummary  `json:"revisions"`
	Approvals  []ApprovalView     `json:"approvals"`
	Decisions  []DecisionRecord   `json:"decisions"`
	Artifacts  []Artifact         `json:"artifacts"`
	Provenance []ProvenanceRecord `json:"provenance"`
	Events     []Event            `json:"events"`
	Public     bool               `json:"public"`
	Source     string             `json:"source"`
}

func (a *API) approvalViews(ctx context.Context, c Commission) ([]ApprovalView, error) {
	requests, e := a.Repo.Approvals(ctx, c.ID)
	if e != nil {
		return nil, e
	}
	decisions, e := a.Repo.Decisions(ctx, c.ID)
	if e != nil {
		return nil, e
	}
	events, e := a.Repo.Events(ctx, c.ID, 0, 500)
	if e != nil {
		return nil, e
	}
	expired := map[string]bool{}
	terminalStatus := ""
	for _, ev := range events {
		if ev.Kind == "workflow" && (ev.Type == "workflow.cancelled" || ev.Type == "workflow.failed" || ev.Type == "workflow.rejected") {
			terminalStatus = strings.TrimPrefix(ev.Type, "workflow.")
		}
		if ev.Type == "approval.expired" {
			var detail struct {
				ApprovalID string `json:"approval_id"`
			}
			_ = json.Unmarshal(ev.Detail, &detail)
			expired[detail.ApprovalID] = true
		}
	}
	views := []ApprovalView{}
	for _, req := range requests {
		view := ApprovalView{ApprovalRequest: req, Status: "pending"}
		for _, d := range decisions {
			if d.ApprovalID == req.ID {
				view.Status = "decided"
				view.DecisionID = d.ID
				view.Action = d.Action
			}
		}
		if view.Status == "pending" && expired[req.ID] {
			view.Status = "expired"
		}
		if view.Status == "pending" && terminalStatus != "" {
			view.Status = terminalStatus
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *API) buildRecord(ctx context.Context, c Commission, public bool) (WorkflowRecord, error) {
	state, e := a.currentState(ctx, c)
	if e != nil {
		return WorkflowRecord{}, errWorkflowQuery
	}
	l, e := a.lineage(ctx, c)
	if e != nil {
		return WorkflowRecord{}, e
	}
	events, e := a.Repo.Events(ctx, c.ID, 0, 500)
	if e != nil {
		return WorkflowRecord{}, e
	}
	views, e := a.approvalViews(ctx, c)
	if e != nil {
		return WorkflowRecord{}, e
	}
	rec := WorkflowRecord{
		Commission: PublicCommission{ID: c.ID, Title: c.Brief.Title, Objective: c.Brief.Objective, Audience: c.Brief.Audience, CreatedAt: c.CreatedAt},
		Workflow:   state, Runs: l.Runs, Approvals: views, Decisions: l.Decisions, Artifacts: l.Artifacts, Provenance: l.Provenance, Events: events, Public: public, Source: "temporal+postgresql",
		Steps: []StepSummary{}, Revisions: []RevisionSummary{},
	}
	for _, s := range l.Steps {
		rec.Steps = append(rec.Steps, StepSummary{StepID: s.StepID, RunID: s.RunID, Capability: s.Capability, RevisionNumber: s.RevisionNumber, Provider: s.Provenance.Provider, Model: s.Provenance.Model, Live: s.Provenance.Live, PromptID: s.Provenance.PromptID, PromptVersion: s.Provenance.PromptVersion, EvaluationsPassed: allPassed(s.Evaluations), CreatedAt: s.CreatedAt})
	}
	for _, rev := range l.Revisions {
		rec.Revisions = append(rec.Revisions, RevisionSummary{ID: rev.ID, Number: rev.Number, ParentID: rev.ParentID, DirectionHash: rev.DirectionHash, Title: rev.Direction.Title, ProducedBy: rev.ProducedBy, RequiresRevision: rev.Critique.RequiresRevision, HardConstraintsPass: rev.Critique.HardConstraintsPass, CreatedAt: rev.CreatedAt})
	}
	if public {
		// A public record never reveals who the owner is or the direction body.
		for i := range rec.Decisions {
			rec.Decisions[i].ActorID = "commission owner"
		}
		rec.Workflow.Direction = nil
		rec.Workflow.Critique = nil
	}
	return rec, nil
}

/* ------------------------------------------------------------------ *
 * Public records
 * ------------------------------------------------------------------ */

func (a *API) publicRecords(w http.ResponseWriter, r *http.Request) {
	publications, e := a.Repo.Publications(r.Context(), 20)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	items := []map[string]any{}
	for _, p := range publications {
		c, e := a.Repo.CommissionByID(r.Context(), p.CommissionID)
		if e != nil {
			continue
		}
		items = append(items, map[string]any{"publication_id": p.ID, "commission_id": c.ID, "title": c.Brief.Title, "published_at": p.CreatedAt})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) publicRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, e := uuid.Parse(id); e != nil {
		failure(w, r, 404, "NOT_FOUND", "Resource does not exist.")
		return
	}
	p, e := a.Repo.Publication(r.Context(), id)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	c, e := a.Repo.CommissionByID(r.Context(), p.CommissionID)
	if e != nil {
		a.repoError(w, r, e)
		return
	}
	rec, e := a.buildRecord(r.Context(), c, true)
	if e != nil {
		a.recordError(w, r, e)
		return
	}
	writeJSON(w, 200, rec)
}

/* ------------------------------------------------------------------ *
 * Server-sent events
 * ------------------------------------------------------------------ */

// events streams state snapshots. NATS hints only prompt a fresh authoritative
// query; a 3-second tick recovers dropped hints and a hash suppresses
// duplicates, so repeated or forged hints cannot change what the client sees.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	c, ok := a.owned(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		failure(w, r, 500, "STREAM_UNAVAILABLE", "Streaming unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	hints := make(chan *nats.Msg, 32)
	if a.NATS != nil {
		if sub, e := a.NATS.ChanSubscribe(ProgressSubject(c.ID), hints); e == nil {
			defer func() { _ = sub.Unsubscribe() }()
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	last := ""
	for {
		state, e := a.currentState(ctx, c)
		if e == nil {
			encoded, _ := json.Marshal(state)
			hash := digest(encoded)
			if hash != last {
				_, _ = fmt.Fprintf(w, "event: state\ndata: %s\n\n", encoded)
				last = hash
			}
		} else {
			_, _ = io.WriteString(w, "event: unavailable\ndata: {\"code\":\"WORKFLOW_QUERY_UNAVAILABLE\"}\n\n")
		}
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		flusher.Flush()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-hints:
		}
	}
}
