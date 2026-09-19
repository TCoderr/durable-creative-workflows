# ADR 0001: Temporal owns execution; PostgreSQL owns the record; capabilities stay stateless

Status: accepted.

Human waits measured in days and recovery after process crashes are primary requirements. Temporal history, activities, signals and timers provide them without rebuilding an engine from status rows or messages. PostgreSQL remains authoritative for the commission, accepted outputs, revisions, approvals, decisions, artifacts, provenance and events; stable identifiers and unique constraints make repeated activity delivery safe.

Core NATS carries expendable progress hints. JetStream, Redis, Kafka, a vector store and a service mesh solve no current requirement and are omitted. The control plane is one modular Go binary with API and worker roles; the Python service isolates probabilistic capability and provider code and owns no durable state.
