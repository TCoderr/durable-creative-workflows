"""Build the reviewed control-plane contract and the schema-derived capability contract.

Run from the repository root with the capability service's Python:
  cd services/capabilities && uv run python ../../docs/api/build.py
No runtime connections or secrets are needed.
"""

import copy
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "services/capabilities"))
from velin_capabilities.app import app  # noqa: E402
from velin_capabilities.schemas import OUTPUT_SCHEMAS, HumanDecision  # noqa: E402

OUT = ROOT / "docs/api"


def ref(name):
    return {"$ref": "#/components/schemas/" + name}


def obj(properties, required=None, **extra):
    return {"type": "object", "properties": properties,
            "required": list(properties) if required is None else required,
            "additionalProperties": False, **extra}


def text(minimum=0, maximum=None, **extra):
    value = {"type": "string", **extra}
    if minimum:
        value["minLength"] = minimum
    if maximum:
        value["maxLength"] = maximum
    return value


def array(items, **extra):
    return {"type": "array", "items": items, **extra}


def response(description, schema, **extra):
    return {"description": description, "content": {"application/json": {"schema": schema}}, **extra}


def save(name, value):
    (OUT / name).write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def build():
    private = app.openapi()
    private["openapi"] = "3.1.1"
    private["info"]["title"] = "VELIN capability service"
    private["info"]["description"] = (
        "Private stateless capability service. Only control-plane activities call this API. "
        "No database credentials, durable state mutation, approval, arbitrary network tools or "
        "browser CORS. Successful output is a proposal plus checks; failed mandatory checks may be "
        "returned with 200 and must block production in the control plane. live:false is a fixture."
    )
    private["servers"] = [{"url": "http://127.0.0.1:18000", "description": "Local Docker host mapping"},
                          {"url": "http://capabilities:8000", "description": "Private Compose network"}]
    private["security"] = [{"ServiceBearer": []}]
    components = private["components"]
    components["securitySchemes"] = {"ServiceBearer": {
        "type": "http", "scheme": "bearer", "description": "CAPABILITIES_TOKEN, minimum 32 characters. "
        "Never send this token to a browser or commit it."}}
    schemas = components["schemas"]
    for model in sorted({*OUTPUT_SCHEMAS.values(), HumanDecision}, key=lambda item: item.__name__):
        schema = model.model_json_schema(ref_template="#/components/schemas/{model}")
        schemas.update(schema.pop("$defs", {}))
        schemas[model.__name__] = schema
    schemas["CapabilityResponse"]["properties"]["output"] = {
        "anyOf": [ref(name) for name in sorted({m.__name__ for m in OUTPUT_SCHEMAS.values()})],
        "description": "Strict capability-specific output; see x-capability-output-schemas.",
        "x-capability-output-schemas": {capability: model.__name__ for capability, model in OUTPUT_SCHEMAS.items()},
    }
    schemas["CapabilityRequest"]["properties"]["context"] = obj({
        **{capability: ref(model.__name__) for capability, model in OUTPUT_SCHEMAS.items()},
        "human_decision": ref("HumanDecision"), "review_round": {"type": "integer"},
    }, required=[], description="Only relevant prior results; serialized UTF-8 limit 64 KiB.",
       **{"x-max-json-utf8-bytes": 65536})
    schemas["CapabilityError"] = obj({
        "code": text(1), "message": text(1), "traceId": text(32, 32),
        "tool_calls": array(ref("ToolCall"), maxItems=16),
        "invocations": array(ref("Invocation"), maxItems=8),
    }, required=["code", "message", "traceId"])
    for path, value in private["paths"].items():
        for method, operation in value.items():
            if path.startswith("/health/"):
                operation["security"] = []
                operation["responses"] = {"200": response("Process is alive or initialized.", obj({"status": text(1)}))}
                if path.endswith("ready"):
                    operation["responses"]["503"] = response("Initialization incomplete.",
                        obj({"status": {"const": "not_ready", "type": "string"}}))
            elif path == "/metrics":
                operation["responses"] = {"200": {"description": "Authenticated Prometheus exposition.",
                    "content": {"text/plain": {"schema": {"type": "string"}}}},
                    "401": response("Service authentication required.", ref("CapabilityError"))}
            elif path == "/v1/capabilities/run":
                operation["description"] = (
                    "128 KiB request limit; 16 concurrent calls; 45 s overall deadline. Up to 2 attempts "
                    "per approved provider, 10 s HTTP timeout, one schema repair. Tools are allowlisted "
                    "and read-only. The same step_id may execute again after a crash: durable "
                    "deduplication is the control plane's responsibility. Failures carry only safe "
                    "attempt evidence, never accepted output."
                )
                operation["x-max-request-bytes"] = 131072
                operation["parameters"] = [{"name": "traceparent", "in": "header", "required": False,
                    "schema": {"type": "string"}, "description": "W3C trace context."},
                    {"name": "X-Velin-Test-Fault", "in": "header", "required": False,
                     "schema": {"type": "string", "enum": ["malformed_once", "malformed_always",
                     "primary_timeout", "primary_failure", "force_revision", "tool_failure",
                     "unauthorized_tool", "slow_activity", "prompt_injection", "regressed"]},
                     "description": "Local fault injection only when VELIN_TEST_MODE=1; forbidden "
                     "otherwise. Test mode cannot start in APP_ENV=production."}]
                for status, description in {
                    "401": "Missing/invalid internal bearer.", "403": "Fault injection disabled.",
                    "413": "Request body too large.", "422": "Invalid schema/prompt/tool request, "
                    "permanent provider failure, or exhausted structured repair.",
                    "429": "Capability concurrency limit reached.",
                    "503": "Provider/tool unavailable, circuit open, or capability deadline exceeded.",
                    "500": "Sanitized internal fault.",
                }.items():
                    operation["responses"][status] = response(description, ref("CapabilityError"))
    save("capabilities-openapi.json", private)

    public_schemas = {name: copy.deepcopy(value) for name, value in schemas.items()
                      if name not in {"CapabilityRequest", "CapabilityResponse", "CapabilityError",
                                      "HTTPValidationError", "ValidationError", "Brief", "Memory",
                                      "HumanDecision", "Evaluation"}}
    uid = text(format="uuid")
    identifier = text(1, 128, pattern=r"^[a-zA-Z0-9_.:-]{1,128}$")
    sha = text(64, 64, pattern="^[a-f0-9]{64}$")
    stamp = text(format="date-time")
    memory = obj({"id": identifier, "source": text(1, 300), "value": text(1, 500),
                  "approved": {"type": "boolean", "default": False}},
                 required=["id", "source", "value"], description="User-declared advisory brand "
                 "memory with provenance; approved is not a VELIN decision or entitlement.")
    brief_properties = {
        "title": text(3, 160, **{"x-max-utf8-bytes": 160}),
        "objective": text(15, 2000, **{"x-max-utf8-bytes": 2000}),
        "audience": text(3, 300, **{"x-max-utf8-bytes": 300}),
        "constraints": array(text(1, 500), maxItems=20, default=[]),
        "prohibited": array(text(1, 500), maxItems=20, default=[]),
        "brand_memory": array(ref("Memory"), maxItems=20, default=[]),
    }
    public_schemas["Memory"] = memory
    public_schemas["Brief"] = obj(brief_properties, description="Immutable submitted brief. "
        "Combined serialized brief/advisory memory<=16 KiB. Upper text limits count UTF-8 bytes.",
        **{"x-max-json-utf8-bytes": 16384})
    input_properties = copy.deepcopy(brief_properties)
    for name in ["constraints", "prohibited", "brand_memory"]:
        input_properties[name]["type"] = ["array", "null"]
    public_schemas["BriefInput"] = obj(input_properties, required=["title", "objective", "audience"],
        description="Omitted/null lists normalize to []. Unknown fields rejected. Serialized aggregate<=16 KiB.",
        **{"x-max-json-utf8-bytes": 16384})
    public_schemas["Commission"] = obj({"id": uid, "brief": ref("Brief"), "created_at": stamp})
    public_schemas["CommissionDetail"] = obj({"id": uid, "workflow_id": uid, "brief": ref("Brief"), "created_at": stamp})
    public_schemas["Run"] = obj({"id": uid, "commission_id": uid, "temporal_run_id": text(1, 128),
        "attempt": {"type": "integer", "minimum": 1}, "started_at": stamp},
        description="One Temporal execution. A resume after a worker restart stays inside the run; a later execution is a new attempt.")
    public_schemas["RevisionRef"] = obj({"id": uid, "number": {"type": "integer", "minimum": 1, "maximum": 3}, "direction_hash": sha},
        description="Content-bound revision identity: derived from commission, number and the direction hash.")
    public_schemas["ApprovalRequest"] = obj({"id": uid, "commission_id": uid, "revision_id": uid,
        "round": {"type": "integer", "minimum": 1, "maximum": 3}, "requested_at": stamp, "deadline_at": stamp},
        description="The durable request for a named person to judge one exact revision.")
    public_schemas["ApprovalView"] = obj({**public_schemas["ApprovalRequest"]["properties"],
        "status": {"type": "string", "enum": ["pending", "decided", "expired", "cancelled", "failed", "rejected"]}, "decision_id": identifier, "action": text()},
        required=[*public_schemas["ApprovalRequest"]["properties"], "status"])
    decision_properties = {"decision_id": text(8, 128, pattern=r"^[a-zA-Z0-9_.:-]{8,128}$"),
        "approval_id": uid, "revision_id": uid,
        "action": {"type": "string", "enum": ["APPROVE", "REVISE", "REJECT"]},
        "reason_code": text(2, 80), "reason": text(0, 2000, default="")}
    public_schemas["DecisionInput"] = obj(decision_properties,
        required=["decision_id", "approval_id", "revision_id", "action", "reason_code"],
        description="actor_id may not be submitted; the actor is the authenticated owner. approval_id and "
        "revision_id must name the pending approval request exactly; anything else is STALE_APPROVAL. "
        "REVISE and REJECT require a reason.")
    public_schemas["Decision"] = obj({**decision_properties, "actor_id": text(1, 256), "commission_id": uid, "decided_at": stamp})
    public_schemas["Evaluation"] = obj({"name": text(1), "passed": {"type": "boolean"},
        "mandatory": {"type": "boolean"}, "score": {"type": "number", "minimum": 0, "maximum": 1},
        "detail": text()}, required=["name", "passed", "mandatory"])
    public_schemas["StepResult"] = obj({"step_id": identifier,
        "capability": {"type": "string", "enum": list(OUTPUT_SCHEMAS)},
        "output": copy.deepcopy(schemas["CapabilityResponse"]["properties"]["output"]),
        "provenance": ref("Provenance"), "tool_calls": array(ref("ToolCall")),
        "evaluations": array(ref("Evaluation")), "invocations": array(ref("Invocation"))})
    public_schemas["StepRecord"] = obj({**public_schemas["StepResult"]["properties"], "commission_id": uid, "run_id": uid,
        "revision_number": {"type": "integer", "minimum": 0}, "created_at": stamp})
    public_schemas["StepSummary"] = obj({"step_id": identifier, "run_id": uid, "capability": text(1), "revision_number": {"type": "integer"},
        "provider": text(), "model": text(), "live": {"type": "boolean"}, "prompt_id": text(), "prompt_version": text(),
        "evaluations_passed": {"type": "boolean"}, "created_at": stamp})
    public_schemas["Revision"] = obj({"id": uid, "commission_id": uid, "number": {"type": "integer"}, "parent_id": uid,
        "direction_hash": sha, "direction": ref("CreativeDirection"), "critique": ref("Critique"), "produced_by": identifier, "created_at": stamp},
        required=["id", "commission_id", "number", "direction_hash", "direction", "critique", "produced_by", "created_at"])
    public_schemas["RevisionSummary"] = obj({"id": uid, "number": {"type": "integer"}, "parent_id": uid, "direction_hash": sha, "title": text(),
        "produced_by": identifier, "requires_revision": {"type": "boolean"}, "hard_constraints_pass": {"type": "boolean"}, "created_at": stamp},
        required=["id", "number", "direction_hash", "title", "produced_by", "requires_revision", "hard_constraints_pass", "created_at"])
    public_schemas["Artifact"] = obj({"id": uid, "commission_id": uid, "revision_id": uid, "decision_id": identifier, "sha256": sha, "created_at": stamp})
    public_schemas["ProvenanceInputs"] = obj({"step_ids": array(identifier), "source_ids": array(identifier), "prompt_id": text(),
        "prompt_hash": text(), "brief_hash": text(), "revision_id": text()}, required=["step_ids", "source_ids", "brief_hash"])
    public_schemas["ProvenanceRecord"] = obj({"id": text(1), "commission_id": uid, "run_id": uid, "step_id": text(), "revision_id": text(),
        "capability": text(1), "inputs": ref("ProvenanceInputs"), "output_ref": text(1), "sha256": sha, "approval_id": text(),
        "decision_id": text(), "created_at": stamp}, required=["id", "commission_id", "run_id", "capability", "inputs", "output_ref", "sha256", "created_at"],
        description="Lineage of one produced object: workflow, run, step, revision, producing capability, inputs, output reference, checksum and approval/decision linkage.")
    public_schemas["Event"] = obj({"sequence": {"type": "integer", "format": "int64"}, "id": text(1), "commission_id": uid, "run_id": uid,
        "type": text(1), "kind": {"type": "string", "enum": ["commission", "run", "step", "failure", "recovery", "revision", "approval", "decision", "artifact", "workflow"]},
        "subject": text(), "detail": {"description": "Event-specific sanitized evidence."}, "created_at": stamp},
        required=["sequence", "id", "commission_id", "type", "kind", "subject", "detail", "created_at"])
    stages = ["DRAFT", "BRIEF_ANALYSIS", "RESEARCH", "STRATEGY", "DIRECTION", "TYPOGRAPHY", "MOTION", "IMAGERY", "CRITIQUE",
              "REVISION", "WAITING_FOR_APPROVAL", "APPROVED", "PRODUCTION", "COMPLETED", "REJECTED", "EXPIRED", "CANCELLED", "FAILED"]
    public_schemas["WorkflowState"] = obj({"commission_id": uid, "run_id": uid, "version": {"type": "integer"},
        "stage": {"type": "string", "enum": stages}, "movement": {"type": "string", "enum": ["BRIEF", "EXECUTE", "REVIEW", "APPROVE", "CONTINUE", "DONE"]},
        "terminal": {"type": "boolean"}, "revision_number": {"type": "integer", "minimum": 0, "maximum": 2},
        "review_round": {"type": "integer", "minimum": 0, "maximum": 3}, "revision": ref("RevisionRef"),
        "direction": ref("CreativeDirection"), "critique": ref("Critique"), "pending_approval": ref("ApprovalRequest"),
        "can_approve": {"type": "boolean"}, "decision_id": identifier, "artifact_id": uid, "last_error": text(),
        "steps_completed": {"type": "integer"}, "stale_decisions": {"type": "integer"}},
        required=["commission_id", "version", "stage", "movement", "terminal", "revision_number", "review_round", "can_approve", "steps_completed", "stale_decisions"],
        description="Temporal query snapshot, or immutable terminal evidence after history retention. DRAFT is returned only before an execution starts. pending_approval names the exact revision a decision must cite.")
    public_schemas["PublicCommission"] = obj({"id": uid, "title": text(), "objective": text(), "audience": text(), "created_at": stamp})
    public_schemas["WorkflowRecord"] = obj({"commission": ref("PublicCommission"), "workflow": ref("WorkflowState"), "runs": array(ref("Run")),
        "steps": array(ref("StepSummary")), "revisions": array(ref("RevisionSummary")), "approvals": array(ref("ApprovalView")),
        "decisions": array(ref("Decision")), "artifacts": array(ref("Artifact")), "provenance": array(ref("ProvenanceRecord")),
        "events": array(ref("Event")), "public": {"type": "boolean"}, "source": text()},
        description="Aggregate read model: authoritative Temporal state plus the append-only PostgreSQL record. Public records omit the owner and the direction body.")
    public_schemas["Lineage"] = obj({"commission": ref("Commission"), "brief_hash": sha, "runs": array(ref("Run")), "steps": array(ref("StepRecord")),
        "revisions": array(ref("Revision")), "approvals": array(ref("ApprovalRequest")), "decisions": array(ref("Decision")),
        "artifacts": array(ref("Artifact")), "provenance": array(ref("ProvenanceRecord")), "chain_of_thought_stored": {"type": "boolean", "const": False}})
    public_schemas["FinalArtifact"] = obj({"schema_version": {"const": "2", "type": "string"}, "id": uid, "commission": ref("Commission"),
        "revision": ref("Revision"), "direction": ref("CreativeDirection"), "approval": ref("ApprovalRequest"), "decision": ref("Decision"),
        "steps": array(ref("StepResult")), "decisions": array(ref("Decision")),
        "provenance": obj({"workflow_id": uid, "run_id": uid, "brief_hash": sha, "step_ids": array(identifier), "revision_id": uid, "approval_id": uid, "decision_id": identifier, "policy": text(1)})},
        description="Private content-addressed JSON artifact bound to the approved revision and the approving decision.")
    public_schemas["Error"] = obj({"code": text(1), "message": text(1), "traceId": text(32, 32)})
    public_schemas["StaleApprovalError"] = obj({"code": {"const": "STALE_APPROVAL", "type": "string"}, "message": text(1), "traceId": text(32, 32),
        "pending_approval": ref("ApprovalRequest")}, description="The decision named a different approval or revision; the pending request is returned so the client can refresh.")
    public_schemas["Readiness"] = obj({name: {"type": "boolean"} for name in ["ready", "postgresql", "temporal", "nats_optional"]})
    error_descriptions = {
        400: "VALIDATION_ERROR, IDEMPOTENCY_KEY_REQUIRED; request is non-retryable until corrected.",
        401: "UNAUTHORIZED; missing, invalid or expired configured bearer/OIDC credential.",
        404: "NOT_FOUND; invalid identifier, absent resource or resource owned by another subject.",
        409: "IDEMPOTENCY_CONFLICT, DECISION_CONFLICT, NOT_WAITING, STALE_APPROVAL or EVALUATION_GATE_FAILED.",
        413: "PAYLOAD_TOO_LARGE; public JSON request limit 32 KiB.",
        415: "UNSUPPORTED_MEDIA_TYPE; JSON endpoints require Content-Type:application/json.",
        429: "RATE_LIMITED; per-subject (or per public client address) burst 120, refill 2 requests/second.",
        503: "DEPENDENCY_UNAVAILABLE, TEMPORAL_UNAVAILABLE, WORKFLOW_QUERY_UNAVAILABLE or ARTIFACT_UNAVAILABLE.",
    }
    errors = {str(code): response(description, ref("Error")) for code, description in error_descriptions.items()}
    errors["429"]["headers"] = {"Retry-After": {"description": "Current implementation returns 1 second.", "schema": {"type": "string", "const": "1"}}}
    paths = {}

    def operation(path, method, operation_id, summary, success, description="", body=None, parameters=None,
                  statuses=(401, 404, 429, 503), public=True, path_description=None):
        value = {"operationId": operation_id, "summary": summary, "description": description,
                 "responses": {**success, **{str(code): copy.deepcopy(errors[str(code)]) for code in statuses}}}
        if not public:
            value["security"] = []
        all_parameters = list(parameters or [])
        if "{id}" in path:
            all_parameters.insert(0, {"name": "id", "in": "path", "required": True, "schema": uid,
                "description": path_description or "Commission UUID, identical to the workflow ID. Ownership is enforced server-side."})
        if public:
            all_parameters.append({"name": "traceparent", "in": "header", "required": False, "schema": text(), "description": "Optional W3C trace context."})
        if all_parameters:
            value["parameters"] = all_parameters
        if body:
            value["requestBody"] = {"required": True, "content": {"application/json": {"schema": body}}}
            value["x-max-request-bytes"] = 32768
        paths.setdefault(path, {})[method] = value

    operation("/api/v1/health/live", "get", "versionedLiveness", "Process liveness",
              {"200": response("Alive", obj({"status": text()}))}, statuses=(), public=False)
    operation("/api/v1/health/ready", "get", "versionedReadiness", "Required dependency readiness",
              {"200": response("Ready", ref("Readiness")), "503": response("Unavailable", ref("Readiness"))}, statuses=(), public=False)
    operation("/health/live", "get", "liveness", "Process liveness",
        {"200": response("Process responds.", obj({"status": {"type": "string", "const": "alive"}}))}, statuses=(), public=False)
    operation("/health/ready", "get", "readiness", "Required dependency readiness",
        {"200": response("PostgreSQL and Temporal are healthy.", ref("Readiness")), "503": response("PostgreSQL or Temporal unavailable.", ref("Readiness"))},
        description="NATS is optional and does not fail readiness.", statuses=(), public=False)
    operation("/metrics", "get", "metrics", "Private operational Prometheus metrics",
        {"200": {"description": "Prometheus exposition; keep network-private.", "content": {"text/plain": {"schema": text()}}}}, statuses=(), public=False)
    operation("/api/v1/public/records", "get", "listPublicRecords", "List published workflow records",
        {"200": response("Newest first, at most 20.", obj({"items": array(obj({"publication_id": uid, "commission_id": uid, "title": text(), "published_at": stamp}))}))},
        description="No authentication. Rate limited by client address. Only commissions the owner published appear.", statuses=(429, 503), public=False)
    operation("/api/v1/public/records/{id}", "get", "getPublicRecord", "Read one published workflow record",
        {"200": response("Redacted aggregate record.", ref("WorkflowRecord"))},
        description="No authentication. The owner subject and the direction body are omitted.", statuses=(404, 429, 503), public=False,
        path_description="Publication UUID returned when the owner published the commission.")
    operation("/api/v1/session", "get", "session", "Resolve authenticated identity and provider mode",
        {"200": response("Identity from server-side authentication.", obj({"subject": text(1, 256),
            "mode": {"type": "string", "enum": ["local-bearer", "oidc"]}, "provider_mode": {"type": "string", "enum": ["deterministic", "http"]}}))},
        statuses=(401, 429))
    commission_response = response("Commission owned by the caller.", ref("Commission"),
        headers={"Location": {"schema": text(), "description": "Relative commission resource path."}})
    operation("/api/v1/commissions", "post", "createCommission", "Create an immutable commission",
        {"201": commission_response, "200": copy.deepcopy(commission_response)},
        description="Idempotency key is scoped to the authenticated subject. Same brief/key returns the original with 200; changed content conflicts 409. Creation does not start Temporal.",
        body=obj({"brief": ref("BriefInput")}), parameters=[{"name": "Idempotency-Key", "in": "header", "required": True, "schema": text(8, 128)}],
        statuses=(400, 401, 409, 413, 415, 429, 503))
    operation("/api/v1/commissions", "get", "listCommissions", "List only caller-owned commissions",
        {"200": response("Ordered created_at descending, then UUID.", obj({"items": array(ref("Commission")), "limit": {"type": "integer"}, "offset": {"type": "integer"}}))},
        parameters=[{"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 100, "default": 50}},
                    {"name": "offset", "in": "query", "schema": {"type": "integer", "minimum": 0, "maximum": 10000, "default": 0}}], statuses=(400, 401, 429, 503))
    operation("/api/v1/commissions/{id}", "get", "getCommission", "Read an owned commission",
        {"200": response("Immutable brief and stable workflow identity.", ref("CommissionDetail"))})
    operation("/api/v1/commissions/{id}/start", "post", "startWorkflow", "Start the durable commission workflow",
        {"202": response("Execution started or already exists.", obj({"workflow_id": uid, "commission_id": uid}))},
        description="No body. Temporal workflow ID is the commission UUID; reject-duplicate reuse makes repeated starts safe.")
    operation("/api/v1/commissions/{id}/publications", "post", "publishRecord", "Publish the workflow record",
        {"201": response("Publication created or already existing.", obj({"publication_id": uid, "commission_id": uid, "public_url": text()}))},
        description="Makes the redacted record readable at /api/v1/public/records/{publication_id}. Repeatable.")
    operation("/api/v1/workflows/{id}", "get", "workflowState", "Query authoritative Temporal state",
        {"200": response("Current snapshot; DRAFT if execution does not yet exist.", ref("WorkflowState"))})
    operation("/api/v1/workflows/{id}/record", "get", "workflowRecord", "Read the aggregate workflow record",
        {"200": response("Authoritative state plus the append-only record.", ref("WorkflowRecord"))})
    operation("/api/v1/workflows/{id}/runs", "get", "workflowRuns", "List runs", {"200": response("Runs by attempt.", obj({"items": array(ref("Run"))}))})
    operation("/api/v1/workflows/{id}/approvals", "get", "workflowApprovals", "List approval requests with status",
        {"200": response("Requests by round with derived status.", obj({"items": array(ref("ApprovalView"))}))})
    operation("/api/v1/workflows/{id}/decisions", "get", "workflowDecisions", "List accepted decisions",
        {"200": response("Decisions in acceptance order.", obj({"items": array(ref("Decision"))}))})
    operation("/api/v1/workflows/{id}/timeline", "get", "workflowTimeline", "Read append-only events",
        {"200": response("Ascending sequence, at most 250 events. Continue after the last sequence.", obj({"items": array(ref("Event"), maxItems=250)}))},
        parameters=[{"name": "after", "in": "query", "schema": {"type": "integer", "format": "int64", "minimum": 0, "default": 0}}], statuses=(400, 401, 404, 429, 503))
    operation("/api/v1/workflows/{id}/events", "get", "workflowEvents", "Stream state snapshots via SSE",
        {"200": {"description": "SSE state/unavailable events and heartbeat comments.", "content": {"text/event-stream": {"schema": text()}}},
         "500": response("STREAM_UNAVAILABLE.", ref("Error"))},
        description="NATS hints trigger a fresh Temporal query; 3-second polling recovers dropped or duplicate hints; a hash suppresses repeats. Closes after 2 minutes.")
    paths["/api/v1/workflows/{id}/events"]["get"]["x-sse-events"] = {"state": ref("WorkflowState"),
        "unavailable": obj({"code": {"type": "string", "const": "WORKFLOW_QUERY_UNAVAILABLE"}})}
    operation("/api/v1/workflows/{id}/decisions", "post", "humanDecision", "Approve, revise or reject the exact revision under review",
        {"202": response("Signal delivered, or an identical accepted signal is awaiting decision persistence.", obj({"decision_id": identifier, "approval_id": uid, "revision_id": uid, "status": {"type": "string", "enum": ["signal_sent", "signal_received"]}})),
         "200": response("Identical decision already recorded.", obj({"decision_id": identifier, "status": {"type": "string", "const": "accepted"}}))},
        description="approval_id and revision_id must equal the pending approval request. Anything else is 409 STALE_APPROVAL with the pending request in the body. APPROVE requires can_approve. One accepted decision per approval request.",
        body=ref("DecisionInput"), statuses=(400, 401, 404, 409, 413, 415, 429, 503))
    paths["/api/v1/workflows/{id}/decisions"]["post"]["responses"]["409"] = response("Conflict; STALE_APPROVAL responses carry pending_approval.",
        {"anyOf": [ref("Error"), ref("StaleApprovalError")]})
    operation("/api/v1/workflows/{id}/cancel", "post", "cancelWorkflow", "Request durable workflow cancellation",
        {"202": response("Cancellation requested.", obj({"status": {"type": "string", "const": "cancellation_requested"}}))},
        description="Asynchronous; the workflow ends in the explicit CANCELLED stage. Evidence and artifacts are never deleted.")
    operation("/api/v1/artifacts/{id}", "get", "getArtifact", "Download the private final JSON artifact",
        {"200": response("Owner-authorized, hash-verified bytes.", ref("FinalArtifact"), headers={"ETag": {"schema": text(), "description": "Quoted SHA-256 digest."}})},
        path_description="Artifact UUID. Ownership is enforced through the commission.")
    operation("/api/v1/provenance/{id}", "get", "getProvenance", "Inspect the full lineage of an owned commission",
        {"200": response("Runs, steps, revisions, approvals, decisions, artifacts and provenance rows.", ref("Lineage"))})
    document = {"openapi": "3.1.1", "info": {"title": "VELIN control-plane API", "version": "2.0.0",
        "description": "Implemented Go API. Temporal owns workflow state; PostgreSQL owns the append-only record; NATS carries disposable hints. "
                       "Every private route requires a verified subject and server-side ownership; other owners receive 404. Public record routes "
                       "are read-only and redacted. JSON request limit 32 KiB; unknown fields rejected. No cloud deployment or live model execution is implied."},
        "servers": [{"url": "http://127.0.0.1:8080", "description": "Local Go API. /api/v1 is also proxied same-origin by the frontend on 4173."}],
        "security": [{"UserBearer": []}], "paths": paths,
        "components": {"securitySchemes": {"UserBearer": {"type": "http", "scheme": "bearer",
            "description": "Local opaque credential mapped to a subject, or a configured OIDC JWT verified for issuer, audience, signature and expiry."}},
            "schemas": public_schemas},
        "externalDocs": {"description": "Private capability service contract", "url": "./capabilities-openapi.json"}}
    save("openapi.json", document)
    print(json.dumps({"public_paths": len(paths), "private_paths": len(private["paths"])}))


if __name__ == "__main__":
    build()
