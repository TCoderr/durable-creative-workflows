// Generates infra/observability/grafana/velin.json from a panel list so the
// dashboard stays reviewable as code. Run: node scripts/grafana-dashboard.mjs
import { writeFileSync } from 'node:fs';

const datasource = { type: 'prometheus', uid: 'velin-prometheus' };
const panels = [
  [
    'Workflow outcomes',
    'sum by (outcome) (increase(velin_workflow_outcomes_total[1h]))',
  ],
  ['Active workflows (Temporal visibility)', 'max(velin_workflows_active)'],
  [
    'Workflow duration p95',
    'histogram_quantile(0.95, sum by (outcome, le) (rate(velin_workflow_duration_seconds_bucket[1h])))',
  ],
  [
    'Approval wait p95',
    'histogram_quantile(0.95, sum by (le) (rate(velin_approval_wait_seconds_bucket[1h])))',
  ],
  [
    'Activity retries',
    'sum by (activity) (increase(velin_activity_retries_total[15m]))',
  ],
  [
    'Failed activity attempts',
    'sum by (activity, code) (increase(velin_activity_failures_total[15m]))',
  ],
  [
    'Resumed runs and open workflows at worker start',
    'velin_workflow_runs_resumed_total or velin_worker_open_workflows_at_start',
  ],
  [
    'Replay failures (non-determinism)',
    'sum(increase(temporal_workflow_task_execution_failed{failure_reason="NonDeterminismError"}[15m]))',
  ],
  ['Artifacts produced', 'velin_artifacts_total'],
  ['API request rate', 'sum by (route) (rate(velin_http_requests_total[5m]))'],
  [
    'API p95 latency',
    'histogram_quantile(0.95, sum by (le) (rate(velin_http_duration_seconds_bucket[5m])))',
  ],
  [
    'Capability p95 latency',
    'histogram_quantile(0.95, sum by (capability, le) (rate(velin_capability_duration_seconds_bucket[5m])))',
  ],
  [
    'Provider attempts',
    'sum by (provider, outcome) (rate(velin_provider_calls_total[5m]))',
  ],
  ['Provider circuit state', 'velin_provider_circuit_open'],
  [
    'Schema rejections and repairs',
    'sum by (capability) (increase(velin_schema_failures_total[15m])) or sum by (capability) (increase(velin_structured_repairs_total[15m]))',
  ],
  ['NATS hint failures', 'velin_nats_publish_failures_total'],
];

const dashboard = {
  title: 'VELIN · Durable creative workflows',
  uid: 'velin-runtime',
  schemaVersion: 39,
  version: 1,
  editable: false,
  refresh: '30s',
  time: { from: 'now-6h', to: 'now' },
  tags: ['velin'],
  panels: panels.map(([title, expr], i) => ({
    id: i + 1,
    type: 'timeseries',
    title,
    datasource,
    gridPos: { h: 8, w: 12, x: (i % 2) * 12, y: Math.floor(i / 2) * 8 },
    targets: [{ refId: 'A', expr, datasource }],
    fieldConfig: { defaults: {}, overrides: [] },
    options: { legend: { displayMode: 'list', placement: 'bottom' } },
  })),
};

writeFileSync(
  new URL('../infra/observability/grafana/velin.json', import.meta.url),
  JSON.stringify(dashboard, null, 2) + '\n',
);
console.log(`dashboard written with ${panels.length} panels`);
