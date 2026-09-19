-- Keep all evidence in the commission that owns it, including direct SQL writes.
-- This migration is additive; the applied initial schema remains unchanged.
ALTER TABLE commissions ADD UNIQUE (id, owner_id);
ALTER TABLE workflow_runs ADD UNIQUE (id, commission_id);
ALTER TABLE steps ADD UNIQUE (id, commission_id);
ALTER TABLE revisions ADD UNIQUE (id, commission_id);
ALTER TABLE approvals ADD UNIQUE (id, commission_id, revision_id);
ALTER TABLE decisions ADD UNIQUE (id, commission_id, revision_id);
ALTER TABLE decisions ADD UNIQUE (id, commission_id, revision_id, approval_id);

ALTER TABLE steps ADD FOREIGN KEY (run_id, commission_id) REFERENCES workflow_runs(id, commission_id);
ALTER TABLE revisions ADD FOREIGN KEY (parent_id, commission_id) REFERENCES revisions(id, commission_id);
ALTER TABLE revisions ADD FOREIGN KEY (produced_by, commission_id) REFERENCES steps(id, commission_id);
ALTER TABLE approvals ADD FOREIGN KEY (revision_id, commission_id) REFERENCES revisions(id, commission_id);
ALTER TABLE approvals ADD CHECK (deadline_at > requested_at);
ALTER TABLE decisions ADD FOREIGN KEY (approval_id, commission_id, revision_id) REFERENCES approvals(id, commission_id, revision_id);
ALTER TABLE decisions ADD FOREIGN KEY (commission_id, actor_id) REFERENCES commissions(id, owner_id);
ALTER TABLE artifacts ADD FOREIGN KEY (decision_id, commission_id, revision_id) REFERENCES decisions(id, commission_id, revision_id);
ALTER TABLE provenance ADD FOREIGN KEY (run_id, commission_id) REFERENCES workflow_runs(id, commission_id);
ALTER TABLE provenance ADD FOREIGN KEY (step_id, commission_id) REFERENCES steps(id, commission_id);
ALTER TABLE provenance ADD FOREIGN KEY (revision_id, commission_id) REFERENCES revisions(id, commission_id);
ALTER TABLE provenance ADD FOREIGN KEY (approval_id, commission_id, revision_id) REFERENCES approvals(id, commission_id, revision_id);
ALTER TABLE provenance ADD FOREIGN KEY (decision_id, commission_id, revision_id, approval_id) REFERENCES decisions(id, commission_id, revision_id, approval_id);
ALTER TABLE events ADD FOREIGN KEY (run_id, commission_id) REFERENCES workflow_runs(id, commission_id);

CREATE FUNCTION require_artifact_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM decisions WHERE id = NEW.decision_id
    AND commission_id = NEW.commission_id AND revision_id = NEW.revision_id AND action = 'APPROVE') THEN
    RAISE EXCEPTION 'artifact requires approval of its exact revision' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER artifact_requires_approval BEFORE INSERT ON artifacts
FOR EACH ROW EXECUTE FUNCTION require_artifact_approval();
CREATE TRIGGER immutable_commissions BEFORE UPDATE OR DELETE ON commissions
FOR EACH ROW EXECUTE FUNCTION deny_evidence_mutation();
