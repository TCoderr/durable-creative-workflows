# ADR 0002: Approvals bind to content-addressed revisions

Status: accepted.

A decision that only names a review round can be applied to content the reviewer never saw if a revision lands between the review and the signal. VELIN therefore records a revision as a content-addressed snapshot whose identifier is derived from the commission, its number and the SHA-256 of the direction, and records an approval request that binds a round to exactly one revision.

A decision must cite the pending approval request and its revision. The API refuses anything else with `STALE_APPROVAL` and returns the pending request; the workflow repeats the check before accepting a signal; PostgreSQL enforces it with a composite foreign key from decisions to approvals, and again from artifacts to decisions. The three layers fail independently, so a defect in one cannot record an approval against the wrong content.

Consequences: clients must read `pending_approval` before deciding; a revision requested by a reviewer produces a new approval request and invalidates any decision drafted against the previous one; artifacts are unrepresentable without an approving decision for their exact revision.
