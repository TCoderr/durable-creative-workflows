package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
)

// integrationRepository opens a real PostgreSQL schema for one test and drops
// it afterwards. Without INTEGRATION_DATABASE_URL (or VELIN_INTEGRATION=1 plus
// VELIN_TEST_ENV_FILE) the test skips explicitly.
func integrationRepository(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_DATABASE_URL")
	if dsn == "" && os.Getenv("VELIN_INTEGRATION") == "1" {
		f, e := os.Open(os.Getenv("VELIN_TEST_ENV_FILE"))
		require.NoError(t, e)
		defer f.Close()
		scan := bufio.NewScanner(f)
		password := ""
		port := "5432"
		for scan.Scan() {
			k, v, ok := strings.Cut(scan.Text(), "=")
			if ok && k == "POSTGRES_PASSWORD" {
				password = v
			}
			if ok && k == "VELIN_PORT_POSTGRES" {
				port = v
			}
		}
		require.NoError(t, scan.Err())
		require.NotEmpty(t, password)
		u := url.URL{Scheme: "postgresql", Host: "127.0.0.1:" + port, Path: "/velin", User: url.UserPassword("velin", password), RawQuery: "sslmode=disable"}
		dsn = u.String()
	}
	if dsn == "" {
		t.Skip("set INTEGRATION_DATABASE_URL or VELIN_INTEGRATION=1 plus VELIN_TEST_ENV_FILE to execute real PostgreSQL tests")
	}
	ctx := context.Background()
	admin, e := OpenRepository(ctx, dsn)
	require.NoError(t, e)
	schema := "velin_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	_, e = admin.Pool.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, e)
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	repo, e := OpenRepository(ctx, u.String())
	require.NoError(t, e)
	t.Cleanup(func() {
		repo.Pool.Close()
		if !strings.HasPrefix(schema, "velin_test_") || len(schema) != 43 {
			t.Error("unsafe test cleanup schema")
			return
		}
		_, e := admin.Pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		require.NoError(t, e)
		admin.Pool.Close()
	})
	require.NoError(t, repo.Migrate(ctx))
	return repo
}

func TestIntegrationMigrationIdempotencyConcurrencyAndOwnership(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	require.NoError(t, r.Migrate(ctx))
	var versions int
	require.NoError(t, r.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&versions))
	require.Equal(t, 2, versions)
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, e := r.Create(ctx, "owner-a", "concurrent-key", sampleBrief())
			if e != nil {
				failures <- e
				return
			}
			ids <- c.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for e := range failures {
		require.NoError(t, e)
	}
	id := ""
	for next := range ids {
		if id == "" {
			id = next
		}
		require.Equal(t, id, next)
	}
	require.NotEmpty(t, id)
	_, e := r.Get(ctx, id, "owner-b")
	require.ErrorIs(t, e, ErrNotFound)
	b := sampleBrief()
	b.Title = "Different request"
	_, _, e = r.Create(ctx, "owner-a", "concurrent-key", b)
	require.ErrorIs(t, e, ErrConflict)
	items, e := r.List(ctx, "owner-a", 10, 0)
	require.NoError(t, e)
	require.Len(t, items, 1)
	events, e := r.Events(ctx, id, 0, 10)
	require.NoError(t, e)
	require.Len(t, events, 1)
	require.Equal(t, "commission.received", events[0].Type)
	_, e = r.Pool.Exec(ctx, "UPDATE events SET type='tampered'")
	require.Error(t, e)
	_, e = r.Pool.Exec(ctx, "UPDATE schema_migrations SET sha256=$1", strings.Repeat("0", 64))
	require.NoError(t, e)
	require.ErrorContains(t, r.Migrate(ctx), "checksum changed")
}

func TestIntegrationRunsStepsRevisionsAreIdempotentAndImmutable(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	c, _, e := r.Create(ctx, "owner-a", "evidence-key", sampleBrief())
	require.NoError(t, e)
	run, created, e := r.SaveRun(ctx, c.ID, "temporal-run-1")
	require.NoError(t, e)
	require.True(t, created)
	require.Equal(t, 1, run.Attempt)
	again, created, e := r.SaveRun(ctx, c.ID, "temporal-run-1")
	require.NoError(t, e)
	require.False(t, created)
	require.Equal(t, run.ID, again.ID)
	second, created, e := r.SaveRun(ctx, c.ID, "temporal-run-2")
	require.NoError(t, e)
	require.True(t, created)
	require.Equal(t, 2, second.Attempt)

	in := StepInput{StepID: StepID(c.ID, "research", 0), CommissionID: c.ID, RunID: run.ID, Capability: "research", Brief: c.Brief, Context: map[string]json.RawMessage{}}
	first, e := r.SaveStep(ctx, in, fixture(in))
	require.NoError(t, e)
	late := fixture(in)
	late.Provenance.Model = "late duplicate"
	stored, e := r.SaveStep(ctx, in, late)
	require.NoError(t, e)
	require.Equal(t, first.Provenance.Model, stored.Provenance.Model, "duplicate delivery keeps the first accepted result")
	strategy := StepInput{StepID: StepID(c.ID, "strategy", 0), CommissionID: c.ID, RunID: run.ID, Capability: "strategy", Brief: c.Brief, Context: map[string]json.RawMessage{"research": first.Output}}
	_, e = r.SaveStep(ctx, strategy, fixture(strategy))
	require.NoError(t, e)
	steps, e := r.Steps(ctx, c.ID)
	require.NoError(t, e)
	require.Len(t, steps, 2)
	provenance, e := r.Provenance(ctx, c.ID)
	require.NoError(t, e)
	require.Len(t, provenance, 2)
	require.Equal(t, []string{"source-1"}, provenance[0].Inputs.SourceIDs)
	require.Equal(t, []string{in.StepID}, provenance[1].Inputs.StepIDs, "strategy lineage names the research step it consumed")
	require.Equal(t, run.ID, provenance[1].RunID)

	direction := Direction{Title: "Quiet edition", Objective: "o", Audience: "a", VisualPrinciples: []string{"h"}, Colors: []string{"paper"}, Typography: "serif", Motion: "subtle", Imagery: "still", Composition: []string{"grid"}, Prohibitions: []string{}, References: []string{"source-1"}, Uncertainties: []string{}}
	rev, e := r.SaveRevision(ctx, RevisionInput{CommissionID: c.ID, RunID: run.ID, Number: 1, Direction: direction, Critique: Critique{HardConstraintsPass: true, Summary: "ok"}, ProducedBy: strategy.StepID})
	require.NoError(t, e)
	require.Equal(t, RevisionID(c.ID, 1, HashJSON(direction)), rev.ID)
	same, e := r.SaveRevision(ctx, RevisionInput{CommissionID: c.ID, RunID: run.ID, Number: 1, Direction: direction, Critique: Critique{HardConstraintsPass: true, Summary: "ok"}, ProducedBy: strategy.StepID})
	require.NoError(t, e)
	require.Equal(t, rev.ID, same.ID)
	direction.Title = "Different content for the same number"
	_, e = r.SaveRevision(ctx, RevisionInput{CommissionID: c.ID, RunID: run.ID, Number: 1, Direction: direction, Critique: Critique{}, ProducedBy: strategy.StepID})
	require.ErrorIs(t, e, ErrConflict, "a revision number cannot be rebound to different content")
	for _, table := range []string{"steps", "revisions", "workflow_runs", "provenance"} {
		_, e = r.Pool.Exec(ctx, "DELETE FROM "+table)
		require.Error(t, e, table)
	}
	events, e := r.Events(ctx, c.ID, 0, 50)
	require.NoError(t, e)
	types := []string{}
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	require.Equal(t, []string{"commission.received", "step.completed", "step.completed", "revision.created"}, types)
}

func TestIntegrationApprovalsBindDecisionsToExactRevisions(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	c, _, e := r.Create(ctx, "owner-a", "approval-key", sampleBrief())
	require.NoError(t, e)
	run, _, e := r.SaveRun(ctx, c.ID, "temporal-run-1")
	require.NoError(t, e)
	step := StepInput{StepID: StepID(c.ID, "art_direction", 0), CommissionID: c.ID, RunID: run.ID, Capability: "art_direction", Brief: c.Brief, Context: map[string]json.RawMessage{}}
	_, e = r.SaveStep(ctx, step, fixture(step))
	require.NoError(t, e)
	direction := Direction{Title: "v1", Objective: "o", Audience: "a", VisualPrinciples: []string{"h"}, Colors: []string{"paper"}, Typography: "serif", Motion: "subtle", Imagery: "still", Composition: []string{"grid"}, References: []string{"source-1"}}
	rev1, e := r.SaveRevision(ctx, RevisionInput{CommissionID: c.ID, RunID: run.ID, Number: 1, Direction: direction, Critique: Critique{HardConstraintsPass: true, Summary: "ok"}, ProducedBy: step.StepID})
	require.NoError(t, e)
	direction.Title = "v2"
	rev2, e := r.SaveRevision(ctx, RevisionInput{CommissionID: c.ID, RunID: run.ID, Number: 2, ParentID: rev1.ID, Direction: direction, Critique: Critique{HardConstraintsPass: true, Summary: "ok"}, ProducedBy: step.StepID})
	require.NoError(t, e)
	now := time.Now().UTC().Truncate(time.Second)
	request := ApprovalRequest{ID: ApprovalID(c.ID, 1), CommissionID: c.ID, RevisionID: rev1.ID, Round: 1, RequestedAt: now, DeadlineAt: now.Add(time.Hour)}
	require.NoError(t, r.SaveApproval(ctx, run.ID, request))
	require.NoError(t, r.SaveApproval(ctx, run.ID, request), "repeat delivery is a no-op")
	rebound := request
	rebound.RevisionID = rev2.ID
	require.ErrorIs(t, r.SaveApproval(ctx, run.ID, rebound), ErrConflict, "a round cannot be rebound to another revision")

	// A decision that names the newer revision against round one's approval is
	// rejected by the composite foreign key, not just by application code.
	stale := Decision{ID: "stale-decision-01", ApprovalID: request.ID, RevisionID: rev2.ID, Action: ActionApprove, ReasonCode: "reviewed", ActorID: "owner-a"}
	require.ErrorIs(t, r.SaveDecision(ctx, c, run.ID, stale, 10), ErrStaleDecision)
	decisions, e := r.Decisions(ctx, c.ID)
	require.NoError(t, e)
	require.Empty(t, decisions)

	valid := Decision{ID: "decision-0001", ApprovalID: request.ID, RevisionID: rev1.ID, Action: ActionApprove, ReasonCode: "reviewed", ActorID: "owner-a"}
	require.NoError(t, r.SaveDecision(ctx, c, run.ID, valid, 10))
	require.NoError(t, r.SaveDecision(ctx, c, run.ID, valid, 10), "identical duplicate is accepted")
	changed := valid
	changed.Action = ActionReject
	changed.Reason = "changed my mind"
	require.ErrorIs(t, r.SaveDecision(ctx, c, run.ID, changed, 10), ErrConflict, "first accepted decision wins")
	later := valid
	later.ID = "decision-0002"
	require.ErrorIs(t, r.SaveDecision(ctx, c, run.ID, later, 10), ErrConflict, "one decision per approval request")
	spoof := valid
	spoof.ActorID = "owner-b"
	require.Error(t, r.SaveDecision(ctx, c, run.ID, spoof, 10))
	decisions, e = r.Decisions(ctx, c.ID)
	require.NoError(t, e)
	require.Len(t, decisions, 1)
	require.Equal(t, rev1.ID, decisions[0].RevisionID)

	artifact := Artifact{ID: ArtifactID(c.ID), CommissionID: c.ID, RevisionID: rev1.ID, DecisionID: valid.ID, Hash: strings.Repeat("a", 64), StorageKey: strings.Repeat("a", 64) + ".json"}
	require.NoError(t, r.SaveArtifact(ctx, run.ID, artifact, request.ID, []string{step.StepID}))
	require.NoError(t, r.SaveArtifact(ctx, run.ID, artifact, request.ID, []string{step.StepID}))
	reboundArtifact := artifact
	reboundArtifact.StorageKey = "different-reference.json"
	require.ErrorIs(t, r.SaveArtifact(ctx, run.ID, reboundArtifact, request.ID, []string{step.StepID}), ErrConflict)
	artifact.Hash = strings.Repeat("b", 64)
	require.ErrorIs(t, r.SaveArtifact(ctx, run.ID, artifact, request.ID, nil), ErrConflict)
	wrongRevision := Artifact{ID: uuid.NewString(), CommissionID: c.ID, RevisionID: rev2.ID, DecisionID: valid.ID, Hash: strings.Repeat("c", 64), StorageKey: strings.Repeat("c", 64) + ".json"}
	other, _, e := r.Create(ctx, "owner-a", "second-commission", sampleBrief())
	require.NoError(t, e)
	wrongRevision.CommissionID = other.ID
	require.Error(t, r.SaveArtifact(ctx, run.ID, wrongRevision, request.ID, nil), "an artifact cannot cite a decision made for another revision")
	got, e := r.Artifact(ctx, ArtifactID(c.ID), "owner-a")
	require.NoError(t, e)
	require.Equal(t, valid.ID, got.DecisionID)
	require.Equal(t, rev1.ID, got.RevisionID)
	_, e = r.Artifact(ctx, ArtifactID(c.ID), "owner-b")
	require.ErrorIs(t, e, ErrNotFound)
	provenance, e := r.Provenance(ctx, c.ID)
	require.NoError(t, e)
	var lineage *ProvenanceRecord
	for i := range provenance {
		if provenance[i].Capability == "artifact" {
			lineage = &provenance[i]
		}
	}
	require.NotNil(t, lineage)
	require.Equal(t, rev1.ID, lineage.RevisionID)
	require.Equal(t, step.StepID, lineage.StepID)
	require.Equal(t, run.ID, lineage.RunID)
	require.Equal(t, request.ID, lineage.ApprovalID)
	require.Equal(t, valid.ID, lineage.DecisionID)
	require.Equal(t, strings.Repeat("a", 64), lineage.SHA256)
	for _, table := range []string{"approvals", "decisions", "artifacts"} {
		_, e = r.Pool.Exec(ctx, "DELETE FROM "+table)
		require.Error(t, e, table)
	}
}

/* ------------------------------------------------------------------ *
 * Fake Temporal client for API contract tests
 * ------------------------------------------------------------------ */

type encodedValue struct{ value any }

func (v encodedValue) HasValue() bool { return true }
func (v encodedValue) Get(target any) error {
	data, e := json.Marshal(v.value)
	if e != nil {
		return e
	}
	return json.Unmarshal(data, target)
}

type fakeRun struct{ id string }

func (r fakeRun) GetID() string                                                           { return r.id }
func (r fakeRun) GetRunID() string                                                        { return "fake-run" }
func (r fakeRun) Get(context.Context, any) error                                          { return nil }
func (r fakeRun) GetWithOptions(context.Context, any, client.WorkflowRunGetOptions) error { return nil }

// fakeTemporal answers queries from an in-memory state map and records
// signals. Every other method of client.Client is unused by the API.
type fakeTemporal struct {
	client.Client
	mu      sync.Mutex
	states  map[string]WorkflowState
	signals []Decision
	started []string
}

func (f *fakeTemporal) QueryWorkflow(_ context.Context, id, _ string, query string, args ...any) (converter.EncodedValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, ok := f.states[id]
	if !ok {
		return nil, serviceerror.NewNotFound("no execution")
	}
	if query == DecisionReceiptQuery {
		for _, decision := range f.signals {
			if decision.ID == args[0] && state.DecisionID == decision.ID {
				return encodedValue{HashJSON(decision)}, nil
			}
		}
		return encodedValue{""}, nil
	}
	return encodedValue{state}, nil
}
func (f *fakeTemporal) SignalWorkflow(_ context.Context, id, _ string, _ string, arg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, arg.(Decision))
	return nil
}
func (f *fakeTemporal) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ any, _ ...any) (client.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, options.ID)
	return fakeRun{id: options.ID}, nil
}
func (f *fakeTemporal) CancelWorkflow(context.Context, string, string) error { return nil }
func (f *fakeTemporal) CheckHealth(context.Context, *client.CheckHealthRequest) (*client.CheckHealthResponse, error) {
	return &client.CheckHealthResponse{}, nil
}

func TestIntegrationAPIContractBindsDecisionsAndIsolatesOwners(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	store, e := NewFileStore(t.TempDir())
	require.NoError(t, e)
	temporal := &fakeTemporal{states: map[string]WorkflowState{}}
	tokenA := strings.Repeat("a", 32)
	tokenB := strings.Repeat("b", 32)
	api := &API{Config: Config{TaskQueue: "velin-commissions"}, Repo: r, Store: store, Temporal: temporal, Auth: &Authenticator{Keys: map[string]string{tokenA: "owner-a", tokenB: "owner-b"}}, Limits: NewRateLimiter()}
	handler := api.Handler()
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Idempotency-Key", "api-contract-1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/health/live", "/health/ready", "/api/v1/health/live", "/api/v1/health/ready"} {
		require.Equal(t, 200, request(http.MethodGet, path, "", "").Code, path)
	}
	body, _ := json.Marshal(map[string]any{"brief": sampleBrief()})
	w := request(http.MethodPost, "/api/v1/commissions", tokenA, string(body))
	require.Equal(t, 201, w.Code, w.Body.String())
	var c Commission
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &c))
	require.NotEmpty(t, c.ID)
	require.Equal(t, 200, request(http.MethodPost, "/api/v1/commissions", tokenA, string(body)).Code)
	for _, path := range []string{"/api/v1/commissions/" + c.ID, "/api/v1/workflows/" + c.ID, "/api/v1/workflows/" + c.ID + "/record", "/api/v1/workflows/" + c.ID + "/runs", "/api/v1/workflows/" + c.ID + "/approvals", "/api/v1/workflows/" + c.ID + "/decisions", "/api/v1/workflows/" + c.ID + "/timeline", "/api/v1/workflows/" + c.ID + "/events", "/api/v1/provenance/" + c.ID} {
		require.Equal(t, 404, request(http.MethodGet, path, tokenB, "").Code, path)
	}
	for _, suffix := range []string{"decisions", "cancel"} {
		require.Equal(t, 404, request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/"+suffix, tokenB, `{}`).Code)
	}
	require.Equal(t, 404, request(http.MethodPost, "/api/v1/commissions/"+c.ID+"/publications", tokenB, "").Code)

	// Before any execution the state is DRAFT and the record still renders.
	w = request(http.MethodGet, "/api/v1/workflows/"+c.ID, tokenA, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"stage":"DRAFT"`)
	require.Equal(t, 202, request(http.MethodPost, "/api/v1/commissions/"+c.ID+"/start", tokenA, "").Code)
	require.Equal(t, []string{c.ID}, temporal.started)

	// Simulate the workflow waiting on revision 1.
	run, _, e := r.SaveRun(ctx, c.ID, "temporal-run-1")
	require.NoError(t, e)
	step := StepInput{StepID: StepID(c.ID, "art_direction", 0), CommissionID: c.ID, RunID: run.ID, Capability: "art_direction", Brief: c.Brief, Context: map[string]json.RawMessage{}}
	_, e = r.SaveStep(ctx, step, fixture(step))
	require.NoError(t, e)
	direction := Direction{Title: "v1", Objective: "o", Audience: "a", VisualPrinciples: []string{"h"}, Colors: []string{"paper"}, Typography: "serif", Motion: "subtle", Imagery: "still", Composition: []string{"grid"}, References: []string{"source-1"}}
	rev, e := r.SaveRevision(ctx, RevisionInput{CommissionID: c.ID, RunID: run.ID, Number: 1, Direction: direction, Critique: Critique{HardConstraintsPass: true, Summary: "ok"}, ProducedBy: step.StepID})
	require.NoError(t, e)
	now := time.Now().UTC().Truncate(time.Second)
	approval := ApprovalRequest{ID: ApprovalID(c.ID, 1), CommissionID: c.ID, RevisionID: rev.ID, Round: 1, RequestedAt: now, DeadlineAt: now.Add(time.Hour)}
	require.NoError(t, r.SaveApproval(ctx, run.ID, approval))
	temporal.states[c.ID] = WorkflowState{CommissionID: c.ID, RunID: run.ID, Stage: StageWaiting, Movement: MovementReview, ReviewRound: 1, RevisionNumber: 0, Revision: &RevisionRef{ID: rev.ID, Number: 1, DirectionHash: rev.DirectionHash}, PendingApproval: &approval, CanApprove: true, Direction: &direction}

	decisionBody := func(id, approvalID, revisionID, action string) string {
		b, _ := json.Marshal(map[string]any{"decision_id": id, "approval_id": approvalID, "revision_id": revisionID, "action": action, "reason_code": "reviewed", "reason": "Reviewed the exact revision"})
		return string(b)
	}
	w = request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("stale-decision-1", approval.ID, RevisionID(c.ID, 2, "x"), ActionApprove))
	require.Equal(t, 409, w.Code)
	require.Contains(t, w.Body.String(), "STALE_APPROVAL")
	require.Contains(t, w.Body.String(), rev.ID, "the response names the revision actually under review")
	w = request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("stale-decision-2", ApprovalID(c.ID, 2), rev.ID, ActionApprove))
	require.Equal(t, 409, w.Code)
	require.Equal(t, 400, request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, `{"decision_id":"spoofed-decision","actor_id":"owner-b"}`).Code)
	temporal.states[c.ID] = withCanApprove(temporal.states[c.ID], false)
	w = request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("gated-decision-1", approval.ID, rev.ID, ActionApprove))
	require.Equal(t, 409, w.Code)
	require.Contains(t, w.Body.String(), "EVALUATION_GATE_FAILED")
	temporal.states[c.ID] = withCanApprove(temporal.states[c.ID], true)
	w = request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("approve-decision-1", approval.ID, rev.ID, ActionApprove))
	require.Equal(t, 202, w.Code, w.Body.String())
	require.Len(t, temporal.signals, 1)
	require.Equal(t, "owner-a", temporal.signals[0].ActorID, "actor comes from authentication")
	require.Equal(t, rev.ID, temporal.signals[0].RevisionID)

	// A retry can race the database activity after Temporal accepts the signal.
	waitingState := temporal.states[c.ID]
	acceptedState := waitingState
	acceptedState.PendingApproval = nil
	acceptedState.DecisionID = "approve-decision-1"
	temporal.states[c.ID] = acceptedState
	require.Equal(t, 202, request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("approve-decision-1", approval.ID, rev.ID, ActionApprove)).Code)
	require.Equal(t, 409, request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("approve-decision-1", approval.ID, rev.ID, ActionReject)).Code)
	require.Len(t, temporal.signals, 1, "an exact received retry does not enqueue another signal")
	temporal.states[c.ID] = waitingState

	// Once recorded, the same decision id is idempotent and a changed payload conflicts.
	require.NoError(t, r.SaveDecision(ctx, Commission{ID: c.ID, OwnerID: "owner-a"}, run.ID, temporal.signals[0], 10))
	require.Equal(t, 200, request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("approve-decision-1", approval.ID, rev.ID, ActionApprove)).Code)
	require.Equal(t, 409, request(http.MethodPost, "/api/v1/workflows/"+c.ID+"/decisions", tokenA, decisionBody("approve-decision-1", approval.ID, rev.ID, ActionReject)).Code)

	w = request(http.MethodGet, "/api/v1/workflows/"+c.ID+"/approvals", tokenA, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"status":"decided"`)
	w = request(http.MethodGet, "/api/v1/workflows/"+c.ID+"/record", tokenA, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var record WorkflowRecord
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &record))
	require.Equal(t, StageWaiting, record.Workflow.Stage)
	require.Len(t, record.Runs, 1)
	require.Len(t, record.Steps, 1)
	require.Len(t, record.Revisions, 1)
	require.Len(t, record.Approvals, 1)
	require.Len(t, record.Decisions, 1)
	require.False(t, record.Public)
	require.NotNil(t, record.Workflow.Direction)

	// Publication exposes a redacted record without authentication.
	w = request(http.MethodPost, "/api/v1/commissions/"+c.ID+"/publications", tokenA, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	var published struct {
		PublicationID string `json:"publication_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &published))
	w = request(http.MethodGet, "/api/v1/public/records/"+published.PublicationID, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	// Omitted JSON fields must not retain values from the private response.
	record = WorkflowRecord{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &record))
	require.NotContains(t, w.Body.String(), `"direction":`)
	require.True(t, record.Public)
	require.Nil(t, record.Workflow.Direction, "public records omit the direction body")
	require.Equal(t, "commission owner", record.Decisions[0].ActorID)
	require.NotContains(t, w.Body.String(), "owner-a")
	w = request(http.MethodGet, "/api/v1/public/records", "", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), published.PublicationID)
	require.Equal(t, 404, request(http.MethodGet, "/api/v1/public/records/"+uuid.NewString(), "", "").Code)
	require.Equal(t, 400, request(http.MethodGet, "/api/v1/commissions?limit=1000", tokenA, "").Code)
	require.Equal(t, 400, request(http.MethodPost, "/api/v1/commissions", tokenA, `{"brief":{"title":"short"}}`).Code)
	require.Equal(t, 404, request(http.MethodGet, "/api/v1/artifacts/"+uuid.NewString(), tokenB, "").Code)
}

func withCanApprove(state WorkflowState, can bool) WorkflowState {
	state.CanApprove = can
	return state
}

func TestIntegrationFailedAttemptIsRecordedAndNeverAccepted(t *testing.T) {
	r := integrationRepository(t)
	c, _, e := r.Create(context.Background(), "owner-a", "failed-attempt", sampleBrief())
	require.NoError(t, e)
	run, _, e := r.SaveRun(context.Background(), c.ID, "temporal-run-1")
	require.NoError(t, e)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"code":"STRUCTURED_OUTPUT_INVALID","message":"PRIVATE_TEST_SECRET","invocations":[{"provider":"deterministic","model":"fixture","attempt":1,"repair":true,"outcome":"PROVIDER_REJECTED","latency_ms":1,"live":false}]}`))
	}))
	defer server.Close()
	a := &Activities{Repo: r, CapabilitiesURL: server.URL, CapabilitiesToken: "test-only-not-external", HTTP: server.Client()}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(a.RunCapability, activity.RegisterOptions{Name: StepActivity})
	_, e = env.ExecuteActivity(StepActivity, StepInput{StepID: StepID(c.ID, "brief", 0), CommissionID: c.ID, RunID: run.ID, Capability: "brief", Brief: c.Brief, Context: map[string]json.RawMessage{}}, "")
	require.ErrorContains(t, e, "STRUCTURED_OUTPUT_INVALID")
	steps, e := r.Steps(context.Background(), c.ID)
	require.NoError(t, e)
	require.Empty(t, steps)
	events, e := r.Events(context.Background(), c.ID, 0, 50)
	require.NoError(t, e)
	found := false
	for _, event := range events {
		if event.Type == "step.failed" {
			found = true
			require.Equal(t, "failure", event.Kind)
			require.Contains(t, string(event.Detail), "STRUCTURED_OUTPUT_INVALID")
			require.Contains(t, string(event.Detail), "invocations")
			require.NotContains(t, string(event.Detail), "PRIVATE_TEST_SECRET")
		}
	}
	require.True(t, found)
}

func TestIntegrationEvidenceCannotCrossCommissionsOrProduceRejectedRevision(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	first, _, e := r.Create(ctx, "owner-a", "scope-first", sampleBrief())
	require.NoError(t, e)
	second, _, e := r.Create(ctx, "owner-b", "scope-second", sampleBrief())
	require.NoError(t, e)
	run, _, e := r.SaveRun(ctx, first.ID, "scope-run")
	require.NoError(t, e)
	step := StepInput{StepID: StepID(first.ID, "art_direction", 0), CommissionID: first.ID, RunID: run.ID, Capability: "art_direction", Brief: first.Brief, Context: map[string]json.RawMessage{}}
	_, e = r.SaveStep(ctx, step, fixture(step))
	require.NoError(t, e)
	rev, e := r.SaveRevision(ctx, RevisionInput{CommissionID: first.ID, RunID: run.ID, Number: 1, Direction: Direction{Title: "Reviewed content"}, ProducedBy: step.StepID})
	require.NoError(t, e)
	now := time.Now().UTC()
	approval := ApprovalRequest{ID: ApprovalID(first.ID, 1), CommissionID: first.ID, RevisionID: rev.ID, Round: 1, RequestedAt: now, DeadlineAt: now.Add(time.Hour)}
	require.NoError(t, r.SaveApproval(ctx, run.ID, approval))
	cross := approval
	cross.ID = ApprovalID(second.ID, 1)
	cross.CommissionID = second.ID
	require.Error(t, r.SaveApproval(ctx, run.ID, cross), "approval cannot review another commission's revision")
	crossStep := step
	crossStep.StepID = StepID(second.ID, "art_direction", 0)
	crossStep.CommissionID = second.ID
	_, e = r.SaveStep(ctx, crossStep, fixture(crossStep))
	require.Error(t, e, "a step cannot cite another commission's run")
	decision := Decision{ID: "scope-rejection", ApprovalID: approval.ID, RevisionID: rev.ID, ActorID: "owner-a", Action: ActionReject, ReasonCode: "reviewed", Reason: "Rejected"}
	require.NoError(t, r.SaveDecision(ctx, first, run.ID, decision, 1))
	a := Artifact{ID: ArtifactID(first.ID), CommissionID: first.ID, RevisionID: rev.ID, DecisionID: decision.ID, Hash: strings.Repeat("a", 64), StorageKey: strings.Repeat("a", 64) + ".json"}
	require.Error(t, r.SaveArtifact(ctx, run.ID, a, approval.ID, []string{step.StepID}), "a rejection cannot authorize production")
	_, e = r.Pool.Exec(ctx, "UPDATE commissions SET owner_id='someone-else' WHERE id=$1", first.ID)
	require.Error(t, e, "the original commission and owner are immutable")
}

func TestIntegrationRetainedTerminalRecordNeverBecomesDraftOrRestarts(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	c, _, e := r.Create(ctx, "owner-a", "retained-record", sampleBrief())
	require.NoError(t, e)
	run, _, e := r.SaveRun(ctx, c.ID, "expired-temporal-history")
	require.NoError(t, e)
	temporal := &fakeTemporal{states: map[string]WorkflowState{}}
	api := &API{Repo: r, Temporal: temporal, Auth: &Authenticator{Keys: map[string]string{strings.Repeat("a", 32): "owner-a"}}, Limits: NewRateLimiter()}
	_, e = api.currentState(ctx, c)
	require.Error(t, e, "a missing history must not masquerade as an unstarted commission")
	final := WorkflowState{CommissionID: c.ID, RunID: run.ID, Version: WorkflowVersion, Stage: StageRejected, Movement: MovementDone, Terminal: true, StepsCompleted: 8}
	require.NoError(t, r.Event(ctx, EventInput{CommissionID: c.ID, RunID: run.ID, EventID: c.ID + ":terminal", Type: "workflow.rejected", Kind: "workflow", Subject: "Rejected", Detail: map[string]any{"snapshot": final}}))
	retained, e := api.currentState(ctx, c)
	require.NoError(t, e)
	require.Equal(t, final, retained)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commissions/"+c.ID+"/start", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	w := httptest.NewRecorder()
	api.Handler().ServeHTTP(w, req)
	require.Equal(t, 202, w.Code)
	require.Empty(t, temporal.started, "the persisted run guards against execution after retention")
}
