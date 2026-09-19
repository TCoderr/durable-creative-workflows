# Threat model

Assets: private briefs, directions under review, accepted decisions, artifacts and their lineage, provider credentials, prompt registry integrity, workflow history. Trust boundaries: browser to API, API to Temporal and PostgreSQL, worker to capability service, capability service to provider and tools, worker to object storage, deployment identity to cloud services.

| Threat | Implemented boundary |
| --- | --- |
| Cross-owner read or IDOR | Owner predicate on every private query; artifact reads join the commission; other subjects receive 404 |
| Stale approval applied to a newer revision | Decisions must cite the pending approval and revision; the workflow refuses others; `decisions(approval_id, revision_id)` references `approvals(id, revision_id)`; artifacts reference `decisions(id, revision_id)` |
| Approval spoofing | Server-derived actor, owner check in workflow and repository, one decision per approval request, first valid decision wins |
| Duplicate start, activity or event | Reject-duplicate workflow id; stable step, revision, approval, artifact and event ids with unique constraints; step reuse on retry |
| Forged or replayed progress hint | NATS hints only trigger authoritative queries; SSE dedupes by hash; timeline is a monotonic sequence |
| Worker crash mid-step | Heartbeat timeout, server-side retry, accepted-step reuse, single run, history replay test |
| Non-deterministic workflow change | Committed history replay in tests and CI; SDK non-determinism metric |
| Prompt injection via sources | Sources are data; fixed tool allowlist; schema, evaluation and approval gates |
| Unauthorized tools | Capability-specific allowlist, bounded arguments, no shell or fetch |
| Provider SSRF or credential forwarding | Operator-configured HTTPS endpoint only, no redirects, bounded time and size |
| Malformed or poisoned output | Strict schema, one repair, Go revalidation, text-only rendering, content-addressed storage with integrity checks |
| Prompt registry tampering | Versioned prompts, hash manifest, read-only runtime, evaluation gate |
| Secret or log leakage | No body or token logging, safe error envelopes, sanitized failure evidence, ignored credentials, secret scan |
| Availability abuse | Body, header and context limits; per-subject and per-address token buckets; capability concurrency cap |
| Artifact exposure | Server-generated digest keys, private store, ownership on retrieval |
| Deployment privilege escalation | Non-root workloads, workload identities, scoped roles, private endpoints, immutable image promotion |

Residual risks: deterministic evaluation does not establish resistance of an arbitrary live model or creative quality; a compromised worker identity can act within its database and storage scope; Temporal history holds private payloads; live identity, cloud networking, monitoring, recovery and spend controls require staging verification before any deployment claim.
