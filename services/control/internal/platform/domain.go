package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// WorkflowVersion identifies the deterministic workflow logic. Bump it together
// with a workflow.GetVersion branch whenever command ordering changes.
const WorkflowVersion = 2

// Brief is the immutable request a commission is created from.
type Brief struct {
	Title       string   `json:"title"`
	Objective   string   `json:"objective"`
	Audience    string   `json:"audience"`
	Constraints []string `json:"constraints"`
	Prohibited  []string `json:"prohibited"`
	BrandMemory []Memory `json:"brand_memory"`
}

// Memory is user-declared advisory brand guidance with a recorded source. It is
// never evidence of a VELIN approval.
type Memory struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Value    string `json:"value"`
	Approved bool   `json:"approved"`
}

var safeIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

func (b Brief) Validate() error {
	if utf8.RuneCountInString(strings.TrimSpace(b.Title)) < 3 || len(b.Title) > 160 || utf8.RuneCountInString(strings.TrimSpace(b.Objective)) < 15 || len(b.Objective) > 2000 || utf8.RuneCountInString(strings.TrimSpace(b.Audience)) < 3 || len(b.Audience) > 300 {
		return errors.New("title, objective and audience are required and must meet length limits")
	}
	for _, list := range [][]string{b.Constraints, b.Prohibited} {
		if len(list) > 20 {
			return errors.New("at most 20 entries are allowed per constraint or memory list")
		}
		for _, v := range list {
			if len(strings.TrimSpace(v)) == 0 || len(v) > 500 {
				return errors.New("constraint and memory entries must contain 1 to 500 bytes")
			}
		}
	}
	if len(b.BrandMemory) > 20 {
		return errors.New("at most 20 memory entries allowed")
	}
	for _, m := range b.BrandMemory {
		if !safeIdentifier.MatchString(m.ID) || len(strings.TrimSpace(m.Source)) < 1 || len(m.Source) > 300 || len(strings.TrimSpace(m.Value)) < 1 || len(m.Value) > 500 {
			return errors.New("invalid brand memory record")
		}
	}
	if data, err := json.Marshal(b); err != nil || len(data) > 16<<10 {
		return errors.New("combined brief and advisory memory must not exceed 16 KiB")
	}
	return nil
}

// Commission is the durable root. Its UUID is also the Temporal workflow ID.
type Commission struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"-"`
	Brief     Brief     `json:"brief"`
	CreatedAt time.Time `json:"created_at"`
}

/* ------------------------------------------------------------------ *
 * Stages, movements and transitions
 * ------------------------------------------------------------------ */

const (
	StageDraft        = "DRAFT"
	StageBrief        = "BRIEF_ANALYSIS"
	StageResearch     = "RESEARCH"
	StageStrategy     = "STRATEGY"
	StageDirection    = "DIRECTION"
	StageTypography   = "TYPOGRAPHY"
	StageMotion       = "MOTION"
	StageImagery      = "IMAGERY"
	StageCritique     = "CRITIQUE"
	StageRevision     = "REVISION"
	StageWaiting      = "WAITING_FOR_APPROVAL"
	StageApproved     = "APPROVED"
	StageProduction   = "PRODUCTION"
	StageCompleted    = "COMPLETED"
	StageRejected     = "REJECTED"
	StageExpired      = "EXPIRED"
	StageCancelled    = "CANCELLED"
	StageFailed       = "FAILED"
	MovementBrief     = "BRIEF"
	MovementExecute   = "EXECUTE"
	MovementReview    = "REVIEW"
	MovementApprove   = "APPROVE"
	MovementContinue  = "CONTINUE"
	MovementDone      = "DONE"
	ActionApprove     = "APPROVE"
	ActionRevise      = "REVISE"
	ActionReject      = "REJECT"
	MaxRevisions      = 3
	MaxReviewRounds   = 3
	RequiredStepCount = 8
)

// transitions is the only legal stage graph. The workflow refuses any other
// move, so a code change that reorders stages fails loudly in tests instead of
// producing an inconsistent record.
var transitions = map[string][]string{
	StageDraft:      {StageBrief, StageCancelled, StageFailed},
	StageBrief:      {StageResearch, StageCancelled, StageFailed},
	StageResearch:   {StageStrategy, StageCancelled, StageFailed},
	StageStrategy:   {StageDirection, StageCancelled, StageFailed},
	StageDirection:  {StageTypography, StageCancelled, StageFailed},
	StageTypography: {StageMotion, StageCancelled, StageFailed},
	StageMotion:     {StageImagery, StageCancelled, StageFailed},
	StageImagery:    {StageCritique, StageCancelled, StageFailed},
	StageCritique:   {StageRevision, StageWaiting, StageCancelled, StageFailed},
	StageRevision:   {StageCritique, StageCancelled, StageFailed},
	StageWaiting:    {StageApproved, StageRevision, StageRejected, StageExpired, StageCancelled, StageFailed},
	StageApproved:   {StageProduction, StageCancelled, StageFailed},
	StageProduction: {StageCompleted, StageCancelled, StageFailed},
}

var terminalStages = map[string]bool{StageCompleted: true, StageRejected: true, StageExpired: true, StageCancelled: true, StageFailed: true}

// IsTerminal reports whether a stage ends the workflow.
func IsTerminal(stage string) bool { return terminalStages[stage] }

// ValidTransition reports whether the stage graph allows from -> to.
func ValidTransition(from, to string) error {
	for _, next := range transitions[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition %s -> %s", from, to)
}

// Movement maps a detailed stage onto the five movements the product shows.
func Movement(stage string) string {
	switch stage {
	case StageDraft, StageBrief:
		return MovementBrief
	case StageResearch, StageStrategy, StageDirection, StageTypography, StageMotion, StageImagery, StageRevision:
		return MovementExecute
	case StageCritique, StageWaiting:
		return MovementReview
	case StageApproved:
		return MovementApprove
	case StageProduction:
		return MovementContinue
	}
	return MovementDone
}

/* ------------------------------------------------------------------ *
 * Steps (capability executions)
 * ------------------------------------------------------------------ */

// StepInput is the typed request for one capability execution. StepID is stable
// across retries and worker restarts, so a repeated activity attempt lands on the
// same accepted row.
type StepInput struct {
	StepID         string                     `json:"step_id"`
	CommissionID   string                     `json:"commission_id"`
	RunID          string                     `json:"run_id"`
	Capability     string                     `json:"capability"`
	Brief          Brief                      `json:"brief"`
	Context        map[string]json.RawMessage `json:"context"`
	RevisionNumber int                        `json:"revision"`
}

// StepID derives the stable identifier of a capability step.
func StepID(commissionID, capability string, revisionNumber int) string {
	return fmt.Sprintf("%s:%s:%d", commissionID, capability, revisionNumber)
}

type CapabilityProvenance struct {
	PromptID      string   `json:"prompt_id"`
	PromptVersion string   `json:"prompt_version"`
	PromptHash    string   `json:"prompt_hash"`
	SchemaVersion string   `json:"schema_version"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Live          bool     `json:"live"`
	InputTokens   *int     `json:"input_tokens"`
	OutputTokens  *int     `json:"output_tokens"`
	CostUSD       *float64 `json:"cost_usd"`
	LatencyMS     float64  `json:"latency_ms"`
}
type Evaluation struct {
	Name      string   `json:"name"`
	Passed    bool     `json:"passed"`
	Mandatory bool     `json:"mandatory"`
	Score     *float64 `json:"score,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// StepResult is the accepted typed output of one capability step.
type StepResult struct {
	StepID      string               `json:"step_id"`
	Capability  string               `json:"capability"`
	Output      json.RawMessage      `json:"output"`
	Provenance  CapabilityProvenance `json:"provenance"`
	ToolCalls   []json.RawMessage    `json:"tool_calls"`
	Evaluations []Evaluation         `json:"evaluations"`
	Invocations []json.RawMessage    `json:"invocations"`
}

// StepRecord is a persisted step with its run linkage.
type StepRecord struct {
	StepResult
	CommissionID   string    `json:"commission_id"`
	RunID          string    `json:"run_id"`
	RevisionNumber int       `json:"revision_number"`
	CreatedAt      time.Time `json:"created_at"`
}

type Direction struct {
	Title            string   `json:"title"`
	Objective        string   `json:"objective"`
	Audience         string   `json:"audience"`
	VisualPrinciples []string `json:"visual_principles"`
	Colors           []string `json:"colors"`
	Typography       string   `json:"typography"`
	Motion           string   `json:"motion"`
	Imagery          string   `json:"imagery"`
	Composition      []string `json:"composition"`
	Prohibitions     []string `json:"prohibitions"`
	References       []string `json:"references"`
	Uncertainties    []string `json:"uncertainties"`
}
type Critique struct {
	Score               float64  `json:"score"`
	RequiresRevision    bool     `json:"requires_revision"`
	Issues              []string `json:"issues"`
	Summary             string   `json:"summary"`
	HardConstraintsPass bool     `json:"hard_constraints_pass"`
}

/* ------------------------------------------------------------------ *
 * Runs, revisions, approvals, decisions
 * ------------------------------------------------------------------ */

// Run is one Temporal execution of a commission workflow. A resume after a
// worker restart stays inside the same run; a fresh execution is a new run.
type Run struct {
	ID            string    `json:"id"`
	CommissionID  string    `json:"commission_id"`
	TemporalRunID string    `json:"temporal_run_id"`
	Attempt       int       `json:"attempt"`
	StartedAt     time.Time `json:"started_at"`
}

// RunID derives the stable run identifier from the Temporal run.
func RunID(commissionID, temporalRunID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("velin:run:"+commissionID+":"+temporalRunID)).String()
}

// Revision is a content-addressed snapshot of the direction under review. Its
// identifier is derived from the commission, the revision number and the hash of
// the direction, so an approval that names a revision names exact content.
type Revision struct {
	ID            string    `json:"id"`
	CommissionID  string    `json:"commission_id"`
	Number        int       `json:"number"`
	ParentID      string    `json:"parent_id,omitempty"`
	DirectionHash string    `json:"direction_hash"`
	Direction     Direction `json:"direction"`
	Critique      Critique  `json:"critique"`
	ProducedBy    string    `json:"produced_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// RevisionID derives the content-bound revision identifier.
func RevisionID(commissionID string, number int, directionHash string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("velin:revision:%s:%d:%s", commissionID, number, directionHash))).String()
}

// RevisionRef is the part of a revision the workflow keeps in its state.
type RevisionRef struct {
	ID            string `json:"id"`
	Number        int    `json:"number"`
	DirectionHash string `json:"direction_hash"`
}

// ApprovalRequest is the durable request for a named person to judge one exact
// revision. It is written when the workflow starts waiting.
type ApprovalRequest struct {
	ID           string    `json:"id"`
	CommissionID string    `json:"commission_id"`
	RevisionID   string    `json:"revision_id"`
	Round        int       `json:"round"`
	RequestedAt  time.Time `json:"requested_at"`
	DeadlineAt   time.Time `json:"deadline_at"`
}

// ApprovalID derives the stable approval identifier for a review round.
func ApprovalID(commissionID string, round int) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("velin:approval:%s:%d", commissionID, round))).String()
}

// Decision is a human judgement bound to one approval request and therefore to
// one revision. ActorID always comes from authentication, never from the body.
type Decision struct {
	ID         string `json:"decision_id"`
	ApprovalID string `json:"approval_id"`
	RevisionID string `json:"revision_id"`
	Action     string `json:"action"`
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
	ActorID    string `json:"actor_id,omitempty"`
}

func (d Decision) Validate() error {
	if len(d.ID) < 8 || !safeIdentifier.MatchString(d.ID) {
		return errors.New("decision_id must be 8 to 128 safe characters")
	}
	if _, err := uuid.Parse(d.ApprovalID); err != nil {
		return errors.New("approval_id must identify the pending approval request")
	}
	if _, err := uuid.Parse(d.RevisionID); err != nil {
		return errors.New("revision_id must identify the reviewed revision")
	}
	if d.Action != ActionApprove && d.Action != ActionRevise && d.Action != ActionReject {
		return errors.New("action must be APPROVE, REVISE or REJECT")
	}
	if utf8.RuneCountInString(strings.TrimSpace(d.ReasonCode)) < 2 || len(d.ReasonCode) > 80 || len(d.Reason) > 2000 {
		return errors.New("reason_code is required; reason must not exceed 2000 bytes")
	}
	if d.Action != ActionApprove && utf8.RuneCountInString(strings.TrimSpace(d.Reason)) < 3 {
		return errors.New("a reason is required to revise or reject")
	}
	return nil
}

// ErrStaleDecision marks a decision that names a different approval or revision
// than the one the workflow is waiting on.
var ErrStaleDecision = errors.New("decision does not bind to the pending approval and revision")

// BindsTo checks that a decision applies to exactly the approval and revision
// under review.
func (d Decision) BindsTo(pending *ApprovalRequest) error {
	if pending == nil || d.ApprovalID != pending.ID || d.RevisionID != pending.RevisionID {
		return ErrStaleDecision
	}
	return nil
}

// DecisionRecord is a persisted, accepted decision.
type DecisionRecord struct {
	Decision
	CommissionID string    `json:"commission_id"`
	DecidedAt    time.Time `json:"decided_at"`
}

/* ------------------------------------------------------------------ *
 * Artifacts and provenance
 * ------------------------------------------------------------------ */

type Artifact struct {
	ID           string    `json:"id"`
	CommissionID string    `json:"commission_id"`
	RevisionID   string    `json:"revision_id"`
	DecisionID   string    `json:"decision_id"`
	Hash         string    `json:"sha256"`
	StorageKey   string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// ArtifactID derives the single artifact identifier of a commission.
func ArtifactID(commissionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("velin:artifact:"+commissionID)).String()
}

// ProvenanceInputs lists what an output was produced from.
type ProvenanceInputs struct {
	StepIDs    []string `json:"step_ids"`
	SourceIDs  []string `json:"source_ids"`
	PromptID   string   `json:"prompt_id,omitempty"`
	PromptHash string   `json:"prompt_hash,omitempty"`
	BriefHash  string   `json:"brief_hash"`
	RevisionID string   `json:"revision_id,omitempty"`
}

// ProvenanceRecord binds one produced object to its workflow, run, step,
// revision, producing capability, inputs, output reference and checksum. Rows
// are append-only and keyed by the output identifier, so a replayed or retried
// activity cannot record a second lineage for the same output.
type ProvenanceRecord struct {
	ID           string           `json:"id"`
	CommissionID string           `json:"commission_id"`
	RunID        string           `json:"run_id"`
	StepID       string           `json:"step_id,omitempty"`
	RevisionID   string           `json:"revision_id,omitempty"`
	Capability   string           `json:"capability"`
	Inputs       ProvenanceInputs `json:"inputs"`
	OutputRef    string           `json:"output_ref"`
	SHA256       string           `json:"sha256"`
	ApprovalID   string           `json:"approval_id,omitempty"`
	DecisionID   string           `json:"decision_id,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
}

/* ------------------------------------------------------------------ *
 * Events
 * ------------------------------------------------------------------ */

// EventInput is one append-only progress event. EventID is stable so duplicate
// delivery is absorbed by the unique constraint.
type EventInput struct {
	CommissionID string         `json:"commission_id"`
	RunID        string         `json:"run_id"`
	TraceParent  string         `json:"trace_parent,omitempty"`
	EventID      string         `json:"event_id"`
	Type         string         `json:"type"`
	Kind         string         `json:"kind"`
	Subject      string         `json:"subject"`
	Detail       map[string]any `json:"detail"`
}

type Event struct {
	Sequence     int64           `json:"sequence"`
	ID           string          `json:"id"`
	CommissionID string          `json:"commission_id"`
	RunID        string          `json:"run_id,omitempty"`
	Type         string          `json:"type"`
	Kind         string          `json:"kind"`
	Subject      string          `json:"subject"`
	Detail       json.RawMessage `json:"detail"`
	CreatedAt    time.Time       `json:"created_at"`
}

/* ------------------------------------------------------------------ *
 * Workflow state and inputs
 * ------------------------------------------------------------------ */

// WorkflowState is the Temporal query snapshot. It is the authoritative view of
// where a commission stands; PostgreSQL holds the append-only record.
type WorkflowState struct {
	CommissionID    string           `json:"commission_id"`
	RunID           string           `json:"run_id,omitempty"`
	Version         int              `json:"version"`
	Stage           string           `json:"stage"`
	Movement        string           `json:"movement"`
	Terminal        bool             `json:"terminal"`
	RevisionNumber  int              `json:"revision_number"`
	ReviewRound     int              `json:"review_round"`
	Revision        *RevisionRef     `json:"revision,omitempty"`
	Direction       *Direction       `json:"direction,omitempty"`
	Critique        *Critique        `json:"critique,omitempty"`
	PendingApproval *ApprovalRequest `json:"pending_approval,omitempty"`
	CanApprove      bool             `json:"can_approve"`
	DecisionID      string           `json:"decision_id,omitempty"`
	ArtifactID      string           `json:"artifact_id,omitempty"`
	LastError       string           `json:"last_error,omitempty"`
	StepsCompleted  int              `json:"steps_completed"`
	StaleDecisions  int              `json:"stale_decisions"`
}

type WorkflowInput struct {
	Commission    Commission
	OwnerID       string
	TraceParent   string
	ReviewTimeout time.Duration
}
type RunInput struct {
	CommissionID  string
	TemporalRunID string
	TraceParent   string
}
type RevisionInput struct {
	CommissionID string
	RunID        string
	Number       int
	ParentID     string
	Direction    Direction
	Critique     Critique
	ProducedBy   string
	TraceParent  string
}
type ApprovalInput struct {
	CommissionID string
	RunID        string
	RevisionID   string
	Round        int
	RequestedAt  time.Time
	DeadlineAt   time.Time
	TraceParent  string
}
type DecisionInput struct {
	CommissionID string
	RunID        string
	OwnerID      string
	Decision     Decision
	WaitedMS     int64
	TraceParent  string
}
type ProductionInput struct {
	Commission  Commission
	OwnerID     string
	RunID       string
	Revision    RevisionRef
	Direction   Direction
	Approval    ApprovalRequest
	Decision    Decision
	StepIDs     []string
	TraceParent string
}

/* ------------------------------------------------------------------ *
 * Hashing and validation
 * ------------------------------------------------------------------ */

func HashJSON(v any) string {
	data, _ := json.Marshal(v)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ValidateStepResult accepts only a result that answers this exact step with
// complete provenance and a structurally complete typed output.
func ValidateStepResult(in StepInput, r StepResult) error {
	if r.StepID != in.StepID || r.Capability != in.Capability || !json.Valid(r.Output) {
		return errors.New("invalid step result identity or JSON")
	}
	p := r.Provenance
	if p.PromptID == "" || p.PromptVersion == "" || len(p.PromptHash) != 64 || p.SchemaVersion == "" || p.Provider == "" || p.Model == "" {
		return errors.New("incomplete capability provenance")
	}
	if _, err := hex.DecodeString(p.PromptHash); err != nil {
		return errors.New("invalid prompt hash")
	}
	if in.Capability == "art_direction" || in.Capability == "revision" {
		var d Direction
		if err := json.Unmarshal(r.Output, &d); err != nil {
			return err
		}
		if d.Title == "" || d.Objective == "" || d.Audience == "" || len(d.VisualPrinciples) == 0 || len(d.Colors) == 0 || d.Typography == "" || d.Motion == "" || d.Imagery == "" || len(d.Composition) == 0 {
			return errors.New("incomplete creative direction")
		}
	}
	if in.Capability == "critique" {
		var c Critique
		if err := json.Unmarshal(r.Output, &c); err != nil {
			return err
		}
		if c.Score < 0 || c.Score > 1 || c.Summary == "" {
			return errors.New("invalid critique")
		}
	}
	return nil
}

// ValidateProduction is the deterministic gate before an artifact is produced.
// Only verifiable structural criteria count; no subjective score can satisfy a
// missing hard constraint.
func ValidateProduction(d Direction, b Brief, c Critique, steps []StepResult) error {
	if !c.HardConstraintsPass {
		return errors.New("hard constraint evaluation did not pass")
	}
	if len(steps) < RequiredStepCount {
		return errors.New("required capability evidence is incomplete")
	}
	latest := map[string]StepResult{}
	for _, r := range steps {
		latest[r.Capability] = r
	}
	for _, capability := range []string{"brief", "research", "strategy", "art_direction", "typography", "motion", "imagery", "critique"} {
		if _, ok := latest[capability]; !ok {
			return fmt.Errorf("missing capability: %s", capability)
		}
	}
	for capability, r := range latest {
		if capability == "art_direction" {
			if _, ok := latest["revision"]; ok {
				continue
			}
		}
		mandatory := 0
		for _, e := range r.Evaluations {
			if e.Mandatory {
				mandatory++
				if !e.Passed {
					return fmt.Errorf("mandatory evaluation failed: %s", e.Name)
				}
			}
		}
		if mandatory == 0 {
			return errors.New("mandatory evaluation evidence absent")
		}
	}
	if len(d.References) == 0 {
		return errors.New("source references required")
	}
	var research struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(latest["research"].Output, &research); err != nil {
		return errors.New("research provenance invalid")
	}
	known := map[string]bool{}
	for _, s := range research.Sources {
		known[s.ID] = true
	}
	for _, ref := range d.References {
		if !known[ref] {
			return errors.New("direction refers to unknown research source")
		}
	}
	creative := strings.ToLower(strings.Join(append([]string{d.Typography, d.Motion, d.Imagery}, append(append(d.VisualPrinciples, d.Composition...), d.Colors...)...), " "))
	for _, banned := range b.Prohibited {
		if strings.Contains(creative, strings.ToLower(banned)) {
			return errors.New("prohibited pattern present in creative direction")
		}
		found := false
		for _, item := range d.Prohibitions {
			if strings.EqualFold(item, banned) {
				found = true
			}
		}
		if !found {
			return errors.New("prohibition policy missing")
		}
	}
	return nil
}

// SourceIDs extracts research source identifiers from a research output.
func SourceIDs(output json.RawMessage) []string {
	var research struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	ids := []string{}
	if json.Unmarshal(output, &research) == nil {
		for _, s := range research.Sources {
			ids = append(ids, s.ID)
		}
	}
	return ids
}
