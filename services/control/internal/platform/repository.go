package platform

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict with recorded evidence")

// Repository owns PostgreSQL access. Every write of evidence is keyed by a
// stable identifier and tolerates duplicate delivery.
type Repository struct{ Pool *pgxpool.Pool }

func OpenRepository(ctx context.Context, url string) (*Repository, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 12
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	if os.Getenv("DATABASE_AUTH") == "azure" {
		credential, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, err
		}
		cfg.BeforeConnect = func(ctx context.Context, config *pgx.ConnConfig) error {
			token, err := credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://ossrdbms-aad.database.windows.net/.default"}})
			if err != nil {
				return err
			}
			config.Password = token.Token
			return nil
		}
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &Repository{Pool: p}, nil
}

func persist(ctx context.Context, operation string) (context.Context, func()) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "persistence."+operation)
	return ctx, func() { span.End() }
}

func (r *Repository) Migrate(ctx context.Context) error {
	c, err := r.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer c.Release()
	if _, err = c.Exec(ctx, "SELECT pg_advisory_lock(84018802)"); err != nil {
		return err
	}
	defer func() { _, _ = c.Exec(context.Background(), "SELECT pg_advisory_unlock(84018802)") }()
	if _, err = c.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY, sha256 char(64) NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		body, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		hash := HashJSON(string(body))
		var existing string
		err = c.QueryRow(ctx, "SELECT sha256 FROM schema_migrations WHERE version=$1", e.Name()).Scan(&existing)
		if err == nil {
			if existing != hash {
				return fmt.Errorf("migration checksum changed: %s", e.Name())
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,sha256) VALUES($1,$2)", e.Name(), hash)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

/* ------------------------------------------------------------------ *
 * Commissions
 * ------------------------------------------------------------------ */

func (r *Repository) Create(ctx context.Context, owner, key string, brief Brief) (Commission, bool, error) {
	ctx, done := persist(ctx, "commission.create")
	defer done()
	id := uuid.NewString()
	body, err := json.Marshal(brief)
	if err != nil {
		return Commission{}, false, err
	}
	hash := HashJSON(brief)
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return Commission{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `INSERT INTO commissions(id,owner_id,idempotency_key,request_hash,brief) VALUES($1,$2,$3,$4,$5) ON CONFLICT(owner_id,idempotency_key) DO NOTHING`, id, owner, key, hash, body)
	if err != nil {
		return Commission{}, false, err
	}
	var c Commission
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT id,owner_id,brief,created_at,request_hash FROM commissions WHERE owner_id=$1 AND idempotency_key=$2`, owner, key).Scan(&c.ID, &c.OwnerID, &c.Brief, &c.CreatedAt, &storedHash)
	if err != nil {
		return Commission{}, false, err
	}
	if storedHash != hash {
		return Commission{}, false, ErrConflict
	}
	if result.RowsAffected() > 0 {
		detail, _ := json.Marshal(map[string]any{"title": brief.Title, "brief_hash": hash})
		_, err = tx.Exec(ctx, `INSERT INTO events(id,commission_id,type,kind,subject,detail) VALUES($1,$2,'commission.received','commission',$3,$4)`, c.ID+":received", c.ID, "Commission "+c.ID, detail)
		if err != nil {
			return Commission{}, false, err
		}
	}
	return c, result.RowsAffected() > 0, tx.Commit(ctx)
}
func (r *Repository) Get(ctx context.Context, id, owner string) (Commission, error) {
	var c Commission
	err := r.Pool.QueryRow(ctx, `SELECT id,owner_id,brief,created_at FROM commissions WHERE id=$1 AND owner_id=$2`, id, owner).Scan(&c.ID, &c.OwnerID, &c.Brief, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}
func (r *Repository) List(ctx context.Context, owner string, limit, offset int) ([]Commission, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id,owner_id,brief,created_at FROM commissions WHERE owner_id=$1 ORDER BY created_at DESC,id LIMIT $2 OFFSET $3`, owner, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Commission{}
	for rows.Next() {
		var c Commission
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.Brief, &c.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

/* ------------------------------------------------------------------ *
 * Runs
 * ------------------------------------------------------------------ */

// SaveRun records one Temporal execution. Repeated calls for the same
// (commission, temporal run) return the existing row.
func (r *Repository) SaveRun(ctx context.Context, commissionID, temporalRunID string) (Run, bool, error) {
	ctx, done := persist(ctx, "run.save")
	defer done()
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return Run{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize attempt numbering per commission.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", commissionID); err != nil {
		return Run{}, false, err
	}
	id := RunID(commissionID, temporalRunID)
	var attempt int
	if err = tx.QueryRow(ctx, "SELECT count(*)+1 FROM workflow_runs WHERE commission_id=$1", commissionID).Scan(&attempt); err != nil {
		return Run{}, false, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO workflow_runs(id,commission_id,temporal_run_id,attempt) VALUES($1,$2,$3,$4) ON CONFLICT(commission_id,temporal_run_id) DO NOTHING`, id, commissionID, temporalRunID, attempt)
	if err != nil {
		return Run{}, false, err
	}
	var run Run
	if err = tx.QueryRow(ctx, `SELECT id,commission_id,temporal_run_id,attempt,started_at FROM workflow_runs WHERE commission_id=$1 AND temporal_run_id=$2`, commissionID, temporalRunID).Scan(&run.ID, &run.CommissionID, &run.TemporalRunID, &run.Attempt, &run.StartedAt); err != nil {
		return Run{}, false, err
	}
	return run, result.RowsAffected() > 0, tx.Commit(ctx)
}
func (r *Repository) Runs(ctx context.Context, commissionID string) ([]Run, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id,commission_id,temporal_run_id,attempt,started_at FROM workflow_runs WHERE commission_id=$1 ORDER BY attempt`, commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Run{}
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.CommissionID, &run.TemporalRunID, &run.Attempt, &run.StartedAt); err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

/* ------------------------------------------------------------------ *
 * Steps
 * ------------------------------------------------------------------ */

func (r *Repository) Step(ctx context.Context, id string) (StepRecord, error) {
	var s StepRecord
	err := r.Pool.QueryRow(ctx, "SELECT result,commission_id,run_id,revision_number,created_at FROM steps WHERE id=$1", id).Scan(&s.StepResult, &s.CommissionID, &s.RunID, &s.RevisionNumber, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// SaveStep accepts a step result once. It writes the step, its provenance row
// and the step.completed event in one transaction; a duplicate delivery returns
// the stored result unchanged.
func (r *Repository) SaveStep(ctx context.Context, in StepInput, result StepResult) (StepResult, error) {
	ctx, done := persist(ctx, "step.save")
	defer done()
	body, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := tx.Exec(ctx, `INSERT INTO steps(id,commission_id,run_id,capability,revision_number,result) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO NOTHING`, result.StepID, in.CommissionID, in.RunID, in.Capability, in.RevisionNumber, body)
	if err != nil {
		return result, err
	}
	var stored StepResult
	if err = tx.QueryRow(ctx, "SELECT result FROM steps WHERE id=$1 AND commission_id=$2", result.StepID, in.CommissionID).Scan(&stored); err != nil {
		return result, err
	}
	if inserted.RowsAffected() > 0 {
		dependencies, err := dependencyStepIDs(ctx, tx, in)
		if err != nil {
			return result, err
		}
		inputs := ProvenanceInputs{StepIDs: dependencies, SourceIDs: []string{}, PromptID: result.Provenance.PromptID, PromptHash: result.Provenance.PromptHash, BriefHash: HashJSON(in.Brief)}
		if in.Capability == "research" {
			inputs.SourceIDs = SourceIDs(result.Output)
		}
		inputsJSON, _ := json.Marshal(inputs)
		if _, err = tx.Exec(ctx, `INSERT INTO provenance(id,commission_id,run_id,step_id,capability,inputs,output_ref,sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO NOTHING`, "step:"+result.StepID, in.CommissionID, in.RunID, result.StepID, in.Capability, inputsJSON, "step:"+result.StepID, HashJSON(result.Output)); err != nil {
			return result, err
		}
		detail, _ := json.Marshal(map[string]any{"step_id": result.StepID, "capability": in.Capability, "revision_number": in.RevisionNumber, "provider": stored.Provenance.Provider, "model": stored.Provenance.Model, "live": stored.Provenance.Live, "prompt_id": stored.Provenance.PromptID, "prompt_version": stored.Provenance.PromptVersion, "evaluations_passed": allPassed(stored.Evaluations)})
		if _, err = tx.Exec(ctx, `INSERT INTO events(id,commission_id,run_id,type,kind,subject,detail) VALUES($1,$2,$3,'step.completed','step',$4,$5) ON CONFLICT(id) DO NOTHING`, result.StepID+":completed", in.CommissionID, in.RunID, fmt.Sprintf("Step %s, revision %d", in.Capability, in.RevisionNumber), detail); err != nil {
			return result, err
		}
	}
	return stored, tx.Commit(ctx)
}

// dependencyStepIDs resolves which accepted steps a step consumed: for every
// capability named in its context, the latest accepted step of that capability
// at or before the current revision. Recorded rows, not assumptions, define
// lineage.
func dependencyStepIDs(ctx context.Context, tx pgx.Tx, in StepInput) ([]string, error) {
	ids := []string{}
	keys := make([]string, 0, len(in.Context))
	for key := range in.Context {
		if key == "human_decision" || key == "review_round" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var id string
		err := tx.QueryRow(ctx, "SELECT id FROM steps WHERE commission_id=$1 AND capability=$2 AND revision_number<=$3 AND id<>$4 ORDER BY revision_number DESC LIMIT 1", in.CommissionID, key, in.RevisionNumber, in.StepID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func allPassed(evaluations []Evaluation) bool {
	for _, e := range evaluations {
		if e.Mandatory && !e.Passed {
			return false
		}
	}
	return true
}

func (r *Repository) Steps(ctx context.Context, commissionID string) ([]StepRecord, error) {
	rows, err := r.Pool.Query(ctx, "SELECT result,commission_id,run_id,revision_number,created_at FROM steps WHERE commission_id=$1 ORDER BY created_at,id", commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StepRecord{}
	for rows.Next() {
		var item StepRecord
		if err := rows.Scan(&item.StepResult, &item.CommissionID, &item.RunID, &item.RevisionNumber, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

/* ------------------------------------------------------------------ *
 * Events
 * ------------------------------------------------------------------ */

// Event appends one progress event. A duplicate id is a no-op.
func (r *Repository) Event(ctx context.Context, in EventInput) error {
	ctx, done := persist(ctx, "event.append")
	defer done()
	body, err := json.Marshal(in.Detail)
	if err != nil {
		return err
	}
	var runID any
	if in.RunID != "" {
		runID = in.RunID
	}
	_, err = r.Pool.Exec(ctx, `INSERT INTO events(id,commission_id,run_id,type,kind,subject,detail) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING`, in.EventID, in.CommissionID, runID, in.Type, in.Kind, in.Subject, body)
	return err
}
func (r *Repository) Events(ctx context.Context, commissionID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 250
	}
	rows, err := r.Pool.Query(ctx, "SELECT sequence,id,commission_id,run_id,type,kind,subject,detail,created_at FROM events WHERE commission_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3", commissionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Event{}
	for rows.Next() {
		var item Event
		var runID *string
		if err := rows.Scan(&item.Sequence, &item.ID, &item.CommissionID, &runID, &item.Type, &item.Kind, &item.Subject, &item.Detail, &item.CreatedAt); err != nil {
			return nil, err
		}
		if runID != nil {
			item.RunID = *runID
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

/* ------------------------------------------------------------------ *
 * Revisions
 * ------------------------------------------------------------------ */

// SaveRevision records the reviewed snapshot. The id is derived from content;
// a repeated call with the same content is idempotent and a call that tries to
// bind the same number to different content is a conflict.
func (r *Repository) SaveRevision(ctx context.Context, in RevisionInput) (Revision, error) {
	ctx, done := persist(ctx, "revision.save")
	defer done()
	hash := HashJSON(in.Direction)
	rev := Revision{ID: RevisionID(in.CommissionID, in.Number, hash), CommissionID: in.CommissionID, Number: in.Number, ParentID: in.ParentID, DirectionHash: hash, Direction: in.Direction, Critique: in.Critique, ProducedBy: in.ProducedBy}
	direction, _ := json.Marshal(in.Direction)
	critique, _ := json.Marshal(in.Critique)
	var parent any
	if in.ParentID != "" {
		parent = in.ParentID
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return rev, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := tx.Exec(ctx, `INSERT INTO revisions(id,commission_id,number,parent_id,direction_hash,direction,critique,produced_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(commission_id,number) DO NOTHING`, rev.ID, in.CommissionID, in.Number, parent, hash, direction, critique, in.ProducedBy)
	if err != nil {
		return rev, err
	}
	var storedID, storedHash string
	if err = tx.QueryRow(ctx, "SELECT id,direction_hash,created_at FROM revisions WHERE commission_id=$1 AND number=$2", in.CommissionID, in.Number).Scan(&storedID, &storedHash, &rev.CreatedAt); err != nil {
		return rev, err
	}
	if storedID != rev.ID || storedHash != hash {
		return rev, ErrConflict
	}
	if inserted.RowsAffected() > 0 {
		inputs, _ := json.Marshal(ProvenanceInputs{StepIDs: []string{in.ProducedBy}, SourceIDs: []string{}, BriefHash: "", RevisionID: in.ParentID})
		if _, err = tx.Exec(ctx, `INSERT INTO provenance(id,commission_id,run_id,step_id,revision_id,capability,inputs,output_ref,sha256) VALUES($1,$2,$3,$4,$5,'revision',$6,$7,$8) ON CONFLICT(id) DO NOTHING`, "revision:"+rev.ID, in.CommissionID, in.RunID, in.ProducedBy, rev.ID, inputs, "revision:"+rev.ID, hash); err != nil {
			return rev, err
		}
		detail, _ := json.Marshal(map[string]any{"revision_id": rev.ID, "number": in.Number, "parent_id": in.ParentID, "direction_hash": hash, "produced_by": in.ProducedBy, "requires_revision": in.Critique.RequiresRevision, "hard_constraints_pass": in.Critique.HardConstraintsPass})
		if _, err = tx.Exec(ctx, `INSERT INTO events(id,commission_id,run_id,type,kind,subject,detail) VALUES($1,$2,$3,'revision.created','revision',$4,$5) ON CONFLICT(id) DO NOTHING`, in.CommissionID+":revision:"+rev.ID, in.CommissionID, in.RunID, fmt.Sprintf("Revision %d", in.Number), detail); err != nil {
			return rev, err
		}
	}
	return rev, tx.Commit(ctx)
}
func (r *Repository) Revision(ctx context.Context, id string) (Revision, error) {
	var rev Revision
	var parent *string
	err := r.Pool.QueryRow(ctx, "SELECT id,commission_id,number,parent_id,direction_hash,direction,critique,produced_by,created_at FROM revisions WHERE id=$1", id).Scan(&rev.ID, &rev.CommissionID, &rev.Number, &parent, &rev.DirectionHash, &rev.Direction, &rev.Critique, &rev.ProducedBy, &rev.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return rev, ErrNotFound
	}
	if parent != nil {
		rev.ParentID = *parent
	}
	return rev, err
}
func (r *Repository) Revisions(ctx context.Context, commissionID string) ([]Revision, error) {
	rows, err := r.Pool.Query(ctx, "SELECT id,commission_id,number,parent_id,direction_hash,direction,critique,produced_by,created_at FROM revisions WHERE commission_id=$1 ORDER BY number", commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Revision{}
	for rows.Next() {
		var rev Revision
		var parent *string
		if err := rows.Scan(&rev.ID, &rev.CommissionID, &rev.Number, &parent, &rev.DirectionHash, &rev.Direction, &rev.Critique, &rev.ProducedBy, &rev.CreatedAt); err != nil {
			return nil, err
		}
		if parent != nil {
			rev.ParentID = *parent
		}
		items = append(items, rev)
	}
	return items, rows.Err()
}

/* ------------------------------------------------------------------ *
 * Approvals and decisions
 * ------------------------------------------------------------------ */

// SaveApproval records the request to judge one revision in one round.
func (r *Repository) SaveApproval(ctx context.Context, runID string, request ApprovalRequest) error {
	ctx, done := persist(ctx, "approval.save")
	defer done()
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := tx.Exec(ctx, `INSERT INTO approvals(id,commission_id,revision_id,round,requested_at,deadline_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(commission_id,round) DO NOTHING`, request.ID, request.CommissionID, request.RevisionID, request.Round, request.RequestedAt, request.DeadlineAt)
	if err != nil {
		return err
	}
	var storedID, storedRevision string
	if err = tx.QueryRow(ctx, "SELECT id,revision_id FROM approvals WHERE commission_id=$1 AND round=$2", request.CommissionID, request.Round).Scan(&storedID, &storedRevision); err != nil {
		return err
	}
	if storedID != request.ID || storedRevision != request.RevisionID {
		return ErrConflict
	}
	if inserted.RowsAffected() > 0 {
		detail, _ := json.Marshal(map[string]any{"approval_id": request.ID, "revision_id": request.RevisionID, "round": request.Round, "deadline_at": request.DeadlineAt})
		if _, err = tx.Exec(ctx, `INSERT INTO events(id,commission_id,run_id,type,kind,subject,detail) VALUES($1,$2,$3,'approval.requested','approval',$4,$5) ON CONFLICT(id) DO NOTHING`, request.CommissionID+":approval:"+request.ID, request.CommissionID, runID, fmt.Sprintf("Approval round %d", request.Round), detail); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (r *Repository) Approvals(ctx context.Context, commissionID string) ([]ApprovalRequest, error) {
	rows, err := r.Pool.Query(ctx, "SELECT id,commission_id,revision_id,round,requested_at,deadline_at FROM approvals WHERE commission_id=$1 ORDER BY round", commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ApprovalRequest{}
	for rows.Next() {
		var item ApprovalRequest
		if err := rows.Scan(&item.ID, &item.CommissionID, &item.RevisionID, &item.Round, &item.RequestedAt, &item.DeadlineAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SaveDecision records one accepted decision per approval request. The actor
// must own the commission and the composite foreign key rejects a decision whose
// revision differs from the approval request it answers.
func (r *Repository) SaveDecision(ctx context.Context, c Commission, runID string, d Decision, waitedMS int64) error {
	ctx, done := persist(ctx, "decision.save")
	defer done()
	if c.OwnerID != d.ActorID {
		return errors.New("decision actor is not commission owner")
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	body, _ := json.Marshal(d)
	_, err = tx.Exec(ctx, `INSERT INTO decisions(id,commission_id,approval_id,revision_id,actor_id,action,reason_code,reason,decision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, c.ID+":"+d.ID, c.ID, d.ApprovalID, d.RevisionID, d.ActorID, d.Action, d.ReasonCode, d.Reason, body)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23503" {
			return ErrStaleDecision
		}
		return err
	}
	var existing Decision
	err = tx.QueryRow(ctx, `SELECT decision FROM decisions WHERE commission_id=$1 AND approval_id=$2`, c.ID, d.ApprovalID).Scan(&existing)
	if err != nil {
		return err
	}
	if HashJSON(existing) != HashJSON(d) {
		return ErrConflict
	}
	detail, _ := json.Marshal(map[string]any{"decision_id": d.ID, "approval_id": d.ApprovalID, "revision_id": d.RevisionID, "action": d.Action, "reason_code": d.ReasonCode, "actor": "commission owner", "waited_ms": waitedMS})
	_, err = tx.Exec(ctx, `INSERT INTO events(id,commission_id,run_id,type,kind,subject,detail) VALUES($1,$2,$3,'decision.recorded','decision',$4,$5) ON CONFLICT(id) DO NOTHING`, c.ID+":decision:"+d.ID, c.ID, runID, "Decision "+d.ID, detail)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) Decisions(ctx context.Context, commissionID string) ([]DecisionRecord, error) {
	rows, err := r.Pool.Query(ctx, "SELECT decision,commission_id,decided_at FROM decisions WHERE commission_id=$1 ORDER BY decided_at,id", commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DecisionRecord{}
	for rows.Next() {
		var item DecisionRecord
		if err := rows.Scan(&item.Decision, &item.CommissionID, &item.DecidedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

/* ------------------------------------------------------------------ *
 * Artifacts and provenance
 * ------------------------------------------------------------------ */

// SaveArtifact records the single artifact of a commission together with its
// lineage row and event. A repeat with the same hash is a no-op; a different
// hash is a conflict.
func (r *Repository) SaveArtifact(ctx context.Context, runID string, a Artifact, approvalID string, stepIDs []string) error {
	ctx, done := persist(ctx, "artifact.save")
	defer done()
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := tx.Exec(ctx, `INSERT INTO artifacts(id,commission_id,revision_id,decision_id,sha256,storage_key) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(commission_id) DO NOTHING`, a.ID, a.CommissionID, a.RevisionID, a.CommissionID+":"+a.DecisionID, a.Hash, a.StorageKey)
	if err != nil {
		return err
	}
	var stored Artifact
	if err = tx.QueryRow(ctx, "SELECT id,revision_id,decision_id,sha256,storage_key FROM artifacts WHERE commission_id=$1", a.CommissionID).Scan(&stored.ID, &stored.RevisionID, &stored.DecisionID, &stored.Hash, &stored.StorageKey); err != nil {
		return err
	}
	if stored.ID != a.ID || stored.RevisionID != a.RevisionID || stored.DecisionID != a.CommissionID+":"+a.DecisionID || stored.Hash != a.Hash || stored.StorageKey != a.StorageKey {
		return ErrConflict
	}
	if inserted.RowsAffected() > 0 {
		var acceptedInputs int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM steps WHERE commission_id=$1 AND id=ANY($2)", a.CommissionID, stepIDs).Scan(&acceptedInputs); err != nil {
			return err
		}
		if len(stepIDs) == 0 || acceptedInputs != len(stepIDs) {
			return ErrConflict
		}
		inputs, _ := json.Marshal(ProvenanceInputs{StepIDs: stepIDs, SourceIDs: []string{}, RevisionID: a.RevisionID})
		if _, err = tx.Exec(ctx, `INSERT INTO provenance(id,commission_id,run_id,revision_id,step_id,capability,inputs,output_ref,sha256,approval_id,decision_id) VALUES($1,$2,$3,$4,(SELECT produced_by FROM revisions WHERE id=$4),'artifact',$5,$6,$7,$8,$9) ON CONFLICT(id) DO NOTHING`, "artifact:"+a.ID, a.CommissionID, runID, a.RevisionID, inputs, a.StorageKey, a.Hash, approvalID, a.CommissionID+":"+a.DecisionID); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]string{"artifact_id": a.ID, "revision_id": a.RevisionID, "decision_id": a.DecisionID, "sha256": a.Hash})
		if _, err = tx.Exec(ctx, `INSERT INTO events(id,commission_id,run_id,type,kind,subject,detail) VALUES($1,$2,$3,'artifact.recorded','artifact',$4,$5) ON CONFLICT(id) DO NOTHING`, a.ID+":recorded", a.CommissionID, runID, "Artifact "+a.ID, detail); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (r *Repository) Artifact(ctx context.Context, id, owner string) (Artifact, error) {
	var a Artifact
	err := r.Pool.QueryRow(ctx, `SELECT a.id,a.commission_id,a.revision_id,a.decision_id,a.sha256,a.storage_key,a.created_at FROM artifacts a JOIN commissions c ON c.id=a.commission_id WHERE a.id=$1 AND c.owner_id=$2`, id, owner).Scan(&a.ID, &a.CommissionID, &a.RevisionID, &a.DecisionID, &a.Hash, &a.StorageKey, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	a.DecisionID = publicDecisionID(a.CommissionID, a.DecisionID)
	return a, err
}

// Decisions are stored under a commission-scoped key; public identifiers drop
// the prefix.
func publicDecisionID(commissionID, stored string) string {
	return strings.TrimPrefix(stored, commissionID+":")
}
func (r *Repository) Artifacts(ctx context.Context, commissionID string) ([]Artifact, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id,commission_id,revision_id,decision_id,sha256,storage_key,created_at FROM artifacts WHERE commission_id=$1`, commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Artifact{}
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.CommissionID, &a.RevisionID, &a.DecisionID, &a.Hash, &a.StorageKey, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.DecisionID = publicDecisionID(a.CommissionID, a.DecisionID)
		items = append(items, a)
	}
	return items, rows.Err()
}
func (r *Repository) Provenance(ctx context.Context, commissionID string) ([]ProvenanceRecord, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id,commission_id,run_id,step_id,revision_id,capability,inputs,output_ref,sha256,approval_id,decision_id,created_at FROM provenance WHERE commission_id=$1 ORDER BY created_at,id`, commissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ProvenanceRecord{}
	for rows.Next() {
		var item ProvenanceRecord
		var stepID, revisionID, approvalID, decisionID *string
		if err := rows.Scan(&item.ID, &item.CommissionID, &item.RunID, &stepID, &revisionID, &item.Capability, &item.Inputs, &item.OutputRef, &item.SHA256, &approvalID, &decisionID, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.StepID = deref(stepID)
		item.RevisionID = deref(revisionID)
		item.ApprovalID = deref(approvalID)
		item.DecisionID = publicDecisionID(item.CommissionID, deref(decisionID))
		items = append(items, item)
	}
	return items, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TerminalSnapshot is an immutable workflow observation, not reconstructed execution.
func (r *Repository) TerminalSnapshot(ctx context.Context, commissionID string) (WorkflowState, error) {
	var state WorkflowState
	var raw []byte
	err := r.Pool.QueryRow(ctx, "SELECT detail->'snapshot' FROM events WHERE commission_id=$1 AND id=$2 AND detail ? 'snapshot'", commissionID, commissionID+":terminal").Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.CommissionID != commissionID || !state.Terminal || !IsTerminal(state.Stage) {
		return WorkflowState{}, ErrConflict
	}
	return state, nil
}

/* ------------------------------------------------------------------ *
 * Publications
 * ------------------------------------------------------------------ */

type Publication struct {
	ID           string    `json:"id"`
	CommissionID string    `json:"commission_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// Publish marks an owned commission as publicly readable. Repeating the call
// returns the existing publication.
func (r *Repository) Publish(ctx context.Context, commissionID string) (Publication, error) {
	ctx, done := persist(ctx, "publication.save")
	defer done()
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("velin:publication:"+commissionID)).String()
	if _, err := r.Pool.Exec(ctx, `INSERT INTO publications(id,commission_id) VALUES($1,$2) ON CONFLICT(commission_id) DO NOTHING`, id, commissionID); err != nil {
		return Publication{}, err
	}
	var p Publication
	err := r.Pool.QueryRow(ctx, `SELECT id,commission_id,created_at FROM publications WHERE commission_id=$1`, commissionID).Scan(&p.ID, &p.CommissionID, &p.CreatedAt)
	return p, err
}
func (r *Repository) Publication(ctx context.Context, id string) (Publication, error) {
	var p Publication
	err := r.Pool.QueryRow(ctx, `SELECT id,commission_id,created_at FROM publications WHERE id=$1`, id).Scan(&p.ID, &p.CommissionID, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
func (r *Repository) Publications(ctx context.Context, limit int) ([]Publication, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id,commission_id,created_at FROM publications ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Publication{}
	for rows.Next() {
		var p Publication
		if err := rows.Scan(&p.ID, &p.CommissionID, &p.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// CommissionByID reads a commission without an ownership predicate. Callers
// must only use it behind the publication gate.
func (r *Repository) CommissionByID(ctx context.Context, id string) (Commission, error) {
	var c Commission
	err := r.Pool.QueryRow(ctx, `SELECT id,owner_id,brief,created_at FROM commissions WHERE id=$1`, id).Scan(&c.ID, &c.OwnerID, &c.Brief, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}
