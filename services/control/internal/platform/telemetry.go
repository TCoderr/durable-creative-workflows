package platform

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.temporal.io/sdk/client"
)

var (
	HTTPRequests        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "velin_http_requests_total", Help: "HTTP requests by route and status class."}, []string{"method", "route", "status"})
	HTTPLatency         = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "velin_http_duration_seconds", Help: "HTTP duration excluding SSE stream lifetime.", Buckets: prometheus.DefBuckets}, []string{"route"})
	CapabilityDuration  = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "velin_capability_duration_seconds", Help: "Capability step attempt duration including the service boundary.", Buckets: []float64{.01, .05, .1, .5, 1, 5, 10, 30, 60}}, []string{"capability"})
	ActivityAttempts    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "velin_activity_attempts_total", Help: "Activity executions, including retries."}, []string{"activity", "retry"})
	ActivityRetries     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "velin_activity_retries_total", Help: "Activity executions with attempt > 1."}, []string{"activity"})
	ActivityFailures    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "velin_activity_failures_total", Help: "Failed activity attempts by classified code."}, []string{"activity", "code"})
	NATSPublishFailures = prometheus.NewCounter(prometheus.CounterOpts{Name: "velin_nats_publish_failures_total", Help: "Disposable progress publication failures."})
	EventsPublished     = prometheus.NewCounter(prometheus.CounterOpts{Name: "velin_progress_hints_published_total", Help: "Progress hints published to NATS."})
	WorkflowTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "velin_workflow_transitions_total", Help: "Recorded stage transitions; retries may repeat."}, []string{"stage"})
	WorkflowsStarted    = prometheus.NewCounter(prometheus.CounterOpts{Name: "velin_workflow_runs_started_total", Help: "New workflow runs recorded."})
	WorkflowsResumed    = prometheus.NewCounter(prometheus.CounterOpts{Name: "velin_workflow_runs_resumed_total", Help: "Runs recorded as a later attempt of the same commission."})
	WorkflowOutcomes    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "velin_workflow_outcomes_total", Help: "Terminal outcomes recorded."}, []string{"outcome"})
	WorkflowDuration    = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "velin_workflow_duration_seconds", Help: "Run duration from start to terminal event, including human waits.", Buckets: []float64{1, 10, 60, 300, 3600, 86400, 604800, 1814400}}, []string{"outcome"})
	ActiveWorkflows     = prometheus.NewGauge(prometheus.GaugeOpts{Name: "velin_workflows_active", Help: "Open commission workflows observed in Temporal visibility; use max across workers."})
	OpenAtStartup       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "velin_worker_open_workflows_at_start", Help: "Open commission workflows found on the task queue when this worker started; they are resumed from history."})
	ApprovalWait        = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "velin_approval_wait_seconds", Help: "Completed durable approval wait duration.", Buckets: []float64{1, 10, 60, 300, 3600, 86400, 604800}})
	Artifacts           = prometheus.NewCounter(prometheus.CounterOpts{Name: "velin_artifacts_total", Help: "Artifacts produced."})
)

func init() {
	prometheus.MustRegister(HTTPRequests, HTTPLatency, CapabilityDuration, ActivityAttempts, ActivityRetries, ActivityFailures, NATSPublishFailures, EventsPublished, WorkflowTransitions, WorkflowsStarted, WorkflowsResumed, WorkflowOutcomes, WorkflowDuration, ActiveWorkflows, OpenAtStartup, ApprovalWait, Artifacts)
}
func MetricsHandler() http.Handler { return promhttp.Handler() }

func InitTelemetry(ctx context.Context, mode, environment string) (func(context.Context) error, error) {
	res := resource.NewWithAttributes("", attribute.String("service.name", "velin-control-"+mode), attribute.String("deployment.environment.name", environment), attribute.String("service.version", env("APP_VERSION", "development")))
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1)))}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		exporter, e := otlptracehttp.New(ctx)
		if e != nil {
			return nil, e
		}
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)))
	}
	p := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(p)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return p.Shutdown, nil
}
func traceContext(ctx context.Context, parent string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": parent})
}
func traceParent(ctx context.Context) string {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c.Get("traceparent")
}

/* ------------------------------------------------------------------ *
 * Temporal SDK metrics -> Prometheus
 * ------------------------------------------------------------------ */

// temporalLabels is the fixed label set every SDK metric is projected onto.
// Tags outside this set are dropped so a metric name always has one schema.
var temporalLabels = []string{"namespace", "task_queue", "worker_type", "workflow_type", "activity_type", "failure_reason", "operation"}

type temporalMetrics struct {
	tags map[string]string
}

var (
	temporalMu       sync.Mutex
	temporalCounters = map[string]*prometheus.CounterVec{}
	temporalGauges   = map[string]*prometheus.GaugeVec{}
	temporalTimers   = map[string]*prometheus.HistogramVec{}
)

// NewTemporalMetricsHandler bridges the Temporal SDK's worker and client
// metrics into the Prometheus registry. Replay failures surface as
// temporal_workflow_task_execution_failed{failure_reason="NonDeterminismError"}.
func NewTemporalMetricsHandler() client.MetricsHandler {
	return &temporalMetrics{tags: map[string]string{}}
}

func (m *temporalMetrics) WithTags(tags map[string]string) client.MetricsHandler {
	merged := make(map[string]string, len(m.tags)+len(tags))
	for k, v := range m.tags {
		merged[k] = v
	}
	for k, v := range tags {
		merged[k] = v
	}
	return &temporalMetrics{tags: merged}
}
func (m *temporalMetrics) labels() []string {
	values := make([]string, len(temporalLabels))
	for i, name := range temporalLabels {
		values[i] = m.tags[name]
	}
	return values
}
func metricName(name string) string {
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.ReplaceAll(name, "-", "_")
	if !strings.HasPrefix(name, "temporal_") {
		name = "temporal_" + name
	}
	return name
}
func (m *temporalMetrics) Counter(name string) client.MetricsCounter {
	temporalMu.Lock()
	defer temporalMu.Unlock()
	key := metricName(name)
	vec, ok := temporalCounters[key]
	if !ok {
		vec = prometheus.NewCounterVec(prometheus.CounterOpts{Name: key, Help: "Temporal SDK counter " + name}, temporalLabels)
		if err := prometheus.Register(vec); err != nil {
			if already, ok := err.(prometheus.AlreadyRegisteredError); ok {
				vec = already.ExistingCollector.(*prometheus.CounterVec)
			}
		}
		temporalCounters[key] = vec
	}
	return temporalCounter{counter: vec.WithLabelValues(m.labels()...)}
}
func (m *temporalMetrics) Gauge(name string) client.MetricsGauge {
	temporalMu.Lock()
	defer temporalMu.Unlock()
	key := metricName(name)
	vec, ok := temporalGauges[key]
	if !ok {
		vec = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: key, Help: "Temporal SDK gauge " + name}, temporalLabels)
		if err := prometheus.Register(vec); err != nil {
			if already, ok := err.(prometheus.AlreadyRegisteredError); ok {
				vec = already.ExistingCollector.(*prometheus.GaugeVec)
			}
		}
		temporalGauges[key] = vec
	}
	return temporalGauge{gauge: vec.WithLabelValues(m.labels()...)}
}
func (m *temporalMetrics) Timer(name string) client.MetricsTimer {
	temporalMu.Lock()
	defer temporalMu.Unlock()
	key := metricName(name) + "_seconds"
	vec, ok := temporalTimers[key]
	if !ok {
		vec = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: key, Help: "Temporal SDK timer " + name, Buckets: []float64{.005, .01, .05, .1, .5, 1, 5, 10, 60, 300, 3600, 86400}}, temporalLabels)
		if err := prometheus.Register(vec); err != nil {
			if already, ok := err.(prometheus.AlreadyRegisteredError); ok {
				vec = already.ExistingCollector.(*prometheus.HistogramVec)
			}
		}
		temporalTimers[key] = vec
	}
	return temporalTimer{observer: vec.WithLabelValues(m.labels()...)}
}

type temporalCounter struct{ counter prometheus.Counter }
type temporalGauge struct{ gauge prometheus.Gauge }
type temporalTimer struct{ observer prometheus.Observer }

func (c temporalCounter) Inc(delta int64)      { c.counter.Add(float64(delta)) }
func (g temporalGauge) Update(value float64)   { g.gauge.Set(value) }
func (t temporalTimer) Record(d time.Duration) { t.observer.Observe(d.Seconds()) }
