-- VELIN durable creative workflows: domain and evidence schema.
-- Temporal history owns execution state. These tables hold the immutable
-- commission, every accepted output, the revisions put under review, the
-- approval requests and decisions bound to them, artifacts, provenance and
-- the append-only progress record.

CREATE TABLE commissions (
 id uuid PRIMARY KEY,
 owner_id text NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 256),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 request_hash char(64) NOT NULL,
 brief jsonb NOT NULL CHECK (jsonb_typeof(brief) = 'object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_id, idempotency_key)
);
CREATE INDEX commissions_owner_created ON commissions(owner_id, created_at DESC, id);

-- One row per Temporal execution of the commission workflow.
CREATE TABLE workflow_runs (
 id uuid PRIMARY KEY,
 commission_id uuid NOT NULL REFERENCES commissions(id),
 temporal_run_id text NOT NULL CHECK (length(temporal_run_id) BETWEEN 1 AND 128),
 attempt integer NOT NULL CHECK (attempt >= 1),
 started_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(commission_id, temporal_run_id),
 UNIQUE(commission_id, attempt)
);

-- Accepted capability outputs. The step identifier is stable across retries.
CREATE TABLE steps (
 id text PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 256),
 commission_id uuid NOT NULL REFERENCES commissions(id),
 run_id uuid NOT NULL REFERENCES workflow_runs(id),
 capability text NOT NULL CHECK (length(capability) BETWEEN 1 AND 64),
 revision_number integer NOT NULL CHECK (revision_number >= 0),
 result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX steps_commission ON steps(commission_id, created_at, id);

-- Content-addressed snapshots put under review. The identifier is derived from
-- (commission, number, direction hash), so it names exact content.
CREATE TABLE revisions (
 id uuid PRIMARY KEY,
 commission_id uuid NOT NULL REFERENCES commissions(id),
 number integer NOT NULL CHECK (number BETWEEN 1 AND 3),
 parent_id uuid REFERENCES revisions(id),
 direction_hash char(64) NOT NULL,
 direction jsonb NOT NULL CHECK (jsonb_typeof(direction) = 'object'),
 critique jsonb NOT NULL CHECK (jsonb_typeof(critique) = 'object'),
 produced_by text NOT NULL REFERENCES steps(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(commission_id, number)
);

-- A request for a named person to judge one exact revision.
CREATE TABLE approvals (
 id uuid PRIMARY KEY,
 commission_id uuid NOT NULL REFERENCES commissions(id),
 revision_id uuid NOT NULL REFERENCES revisions(id),
 round integer NOT NULL CHECK (round BETWEEN 1 AND 3),
 requested_at timestamptz NOT NULL,
 deadline_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(commission_id, round),
 UNIQUE(id, revision_id)
);

-- One accepted decision per approval request. The composite foreign key makes
-- it impossible to record a decision against a revision other than the one
-- the approval request named.
CREATE TABLE decisions (
 id text PRIMARY KEY,
 commission_id uuid NOT NULL REFERENCES commissions(id),
 approval_id uuid NOT NULL UNIQUE,
 revision_id uuid NOT NULL,
 actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256),
 action text NOT NULL CHECK (action IN ('APPROVE','REVISE','REJECT')),
 reason_code text NOT NULL,
 reason text NOT NULL,
 decision jsonb NOT NULL,
 decided_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (approval_id, revision_id) REFERENCES approvals(id, revision_id),
 UNIQUE(id, revision_id)
);

-- The single delivered artifact of a commission, bound to the approved revision
-- and to the decision that approved it.
CREATE TABLE artifacts (
 id uuid PRIMARY KEY,
 commission_id uuid NOT NULL UNIQUE REFERENCES commissions(id),
 revision_id uuid NOT NULL REFERENCES revisions(id),
 decision_id text NOT NULL,
 sha256 char(64) NOT NULL,
 storage_key text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (decision_id, revision_id) REFERENCES decisions(id, revision_id)
);

-- Lineage of every produced object. Keyed by the output identifier so a retried
-- activity cannot record a second lineage for the same output.
CREATE TABLE provenance (
 id text PRIMARY KEY,
 commission_id uuid NOT NULL REFERENCES commissions(id),
 run_id uuid NOT NULL REFERENCES workflow_runs(id),
 step_id text REFERENCES steps(id),
 revision_id uuid REFERENCES revisions(id),
 capability text NOT NULL,
 inputs jsonb NOT NULL CHECK (jsonb_typeof(inputs) = 'object'),
 output_ref text NOT NULL,
 sha256 char(64) NOT NULL,
 approval_id uuid REFERENCES approvals(id),
 decision_id text REFERENCES decisions(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX provenance_commission ON provenance(commission_id, created_at, id);

-- Append-only progress record. Duplicate delivery of the same event id is a
-- no-op, which is what lets activities retry safely.
CREATE TABLE events (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 id text NOT NULL UNIQUE,
 commission_id uuid NOT NULL REFERENCES commissions(id),
 run_id uuid REFERENCES workflow_runs(id),
 type text NOT NULL CHECK (length(type) BETWEEN 1 AND 64),
 kind text NOT NULL CHECK (kind IN ('commission','run','step','failure','recovery','revision','approval','decision','artifact','workflow')),
 subject text NOT NULL,
 detail jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_commission_sequence ON events(commission_id, sequence);

-- A commission the owner chose to show publicly. The public read model
-- strips ownership and advisory memory.
CREATE TABLE publications (
 id uuid PRIMARY KEY,
 commission_id uuid NOT NULL UNIQUE REFERENCES commissions(id),
 created_at timestamptz NOT NULL DEFAULT now()
);

-- Evidence is append-only even if a future repository bug attempts to mutate
-- it. Migrations run with an operator role, never the runtime role.
CREATE FUNCTION deny_evidence_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'evidence is append-only'; END $$;
CREATE TRIGGER immutable_runs BEFORE UPDATE OR DELETE ON workflow_runs FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_steps BEFORE UPDATE OR DELETE ON steps FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_revisions BEFORE UPDATE OR DELETE ON revisions FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_approvals BEFORE UPDATE OR DELETE ON approvals FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_decisions BEFORE UPDATE OR DELETE ON decisions FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_artifacts BEFORE UPDATE OR DELETE ON artifacts FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_provenance BEFORE UPDATE OR DELETE ON provenance FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
CREATE TRIGGER immutable_events BEFORE UPDATE OR DELETE ON events FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
