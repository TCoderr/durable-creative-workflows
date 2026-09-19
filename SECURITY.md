# Security model

Private commissions require a bearer credential. Local identities are randomly generated tokens mapped to subjects in ignored files; production uses a configured HTTPS OIDC issuer, dedicated audience and signature/expiry validation. Local tokens and fault injection are refused in production. The folio keeps the token in tab memory only and never in storage or URLs.

Every private route checks ownership on the server; other subjects receive 404. Decisions derive the actor from authentication and must cite the pending approval request and its revision. The workflow revalidates every decision, and PostgreSQL enforces the approval-to-revision and artifact-to-decision bindings with composite foreign keys, so a stale or spoofed approval cannot be recorded through any path. Public record routes are read-only, rate limited by client address, and strip the owner subject and the direction body.

## Input, output and tools

Request bodies are limited to 32 KiB with unknown fields rejected; the capability service limits requests to 128 KiB and context to 64 KiB. SQL uses parameters. Object keys are server-generated digests; reads verify content integrity and ownership. The only tool is an allowlisted, read-only curated research corpus; retrieved text is data. Structured outputs pass strict schemas with one bounded repair. Failed capability responses cross the boundary as a known code and bounded attempt metadata only; messages, bodies and secrets never enter the record.

The folio and the public page render service strings with `textContent`. The production static server sets a restrictive CSP, frame denial, MIME sniffing protection, referrer restriction and a fixed API proxy destination; it forwards only the headers the API needs and never cookies.

## Secrets and operations

`.local/`, `.env*` except the example, runtime artifacts, virtual environments and Terraform state are ignored. `scripts/secret-scan.mjs` checks the tree for configured local secrets and common credential formats. Logs contain events, stable ids, provider and model labels, timing and classified failures; never tokens, briefs, prompts or provider bodies. Temporal histories contain workflow payloads and must be access-controlled under the operator's data policy.

Local Compose binds loopback only; containers run without root or capabilities where the image allows, with read-only filesystems. Kubernetes manifests carry restricted pod security, workload identity, network policy and probes. Metrics endpoints are private.

## Gates

Before a release: `node scripts/secret-scan.mjs`, `govulncheck`, `npm audit`, the test suites, `bash scripts/validate-infra.sh` and `bash scripts/scan-images.sh` against the exact images. The image gate is zero HIGH and zero CRITICAL findings with no suppressions; a finding is reported, not hidden. Report vulnerabilities privately through the repository owner's contact channel.

The scan script builds a pinned Trivy source release with patched gRPC and OS
packages, then includes that scanner image in the default gate. It is disposable
local/CI tooling, never a deployed application service. Its Docker socket grants
engine access even when mounted read-only; run it only on the trusted local/CI
engine. No provider or cloud credentials are passed to it.

The optional Grafana image (Compose profile `observability`, never a Kubernetes
workload) rebuilds the Grafana server with patched dependencies but must ship
the official PGP-signed Prometheus datasource plugin unmodified so signature
enforcement stays on. Its newest release, v13.1.9, embeds gRPC v1.83.1, which
Trivy reports as `CVE-2026-84445` (HIGH, fixed in gRPC 1.83.2). No newer signed
plugin release exists, and rebuilding the plugin would require loading an
unsigned plugin. The finding is reported by the gate, not suppressed; until
Grafana publishes a plugin built on gRPC 1.83.2 or later, the image gate does not
pass for that optional image. At the last local gate run, the other 10 of 11
images (control, capabilities, frontend, PostgreSQL, Temporal, NATS, Prometheus,
the collector, the init image and the scanner) scanned at 0 HIGH / 0 CRITICAL,
so the project is not fully security-clean and is not described as such. The
dashboard container is loopback-only, anonymous Viewer, read-only, non-root and
without capabilities, and correctness never depends on it. No cloud deployment
has been performed; the Kubernetes, Terraform and Argo CD definitions are
validated offline only.

`govulncheck` also reports module-wide advisories. `GO-2026-5932` concerns the
unmaintained `golang.org/x/crypto/openpgp` package, for which no fix is published.
VELIN does not import it: the verified package and reachable-symbol results are
zero. The shared crypto module remains necessary for other imported packages.
This is documented exposure scope, not a suppression or image-gate exception.
