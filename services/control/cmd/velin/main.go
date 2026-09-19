package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"velin/control/internal/platform"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "velin-control", "version", version()))
	if e := run(); e != nil {
		slog.Error("service startup or runtime failed", "event", "service_failed", "error_type", fmt.Sprintf("%T", e))
		fmt.Fprintln(os.Stderr, "VELIN failed; check configuration, dependency health and the runbook. Details:", safeError(e))
		os.Exit(1)
	}
}
func version() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return "development"
}
func safeError(e error) string { // Configuration errors are deliberately value-free.
	var path *os.PathError
	if errors.As(e, &path) {
		return "filesystem access failed"
	}
	return fmt.Sprintf("%T", e)
}
func run() error {
	if len(os.Args) == 3 && os.Args[1] == "health" {
		return health(os.Args[2])
	}
	if len(os.Args) == 3 && os.Args[1] == "replay" {
		replayer := worker.NewWorkflowReplayer()
		replayer.RegisterWorkflowWithOptions(platform.DurableCommission, workflow.RegisterOptions{Name: platform.WorkflowName})
		if e := replayer.ReplayWorkflowHistoryFromJSONFile(nil, os.Args[2]); e != nil {
			slog.Error("workflow history replay failed", "event", "workflow_replay_failed", "error_type", fmt.Sprintf("%T", e))
			return e
		}
		slog.Info("workflow history replay passed", "event", "workflow_replay_passed")
		return nil
	}
	if len(os.Args) != 2 {
		return errors.New("usage: velin api|worker|migrate | velin health <url> | velin replay <history.json>")
	}
	mode := os.Args[1]
	if mode != "api" && mode != "worker" && mode != "migrate" {
		return errors.New("unknown mode")
	}
	cfg, e := platform.LoadConfig()
	if e != nil {
		return e
	}
	slog.SetDefault(slog.Default().With("environment", cfg.Environment, "mode", mode))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startCancel()
	repo, e := platform.OpenRepository(startCtx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	defer repo.Pool.Close()
	if mode == "migrate" {
		e = repo.Migrate(startCtx)
		if e == nil {
			slog.Info("migrations applied", "event", "migrations_applied")
		}
		return e
	}
	shutdownTelemetry, e := platform.InitTelemetry(startCtx, mode, cfg.Environment)
	if e != nil {
		return e
	}
	defer func() {
		flush, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = shutdownTelemetry(flush)
	}()
	options := client.Options{HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace, MetricsHandler: platform.NewTemporalMetricsHandler()}
	if cfg.TemporalCert != "" {
		cert, e := tls.LoadX509KeyPair(cfg.TemporalCert, cfg.TemporalKey)
		if e != nil {
			return e
		}
		options.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	temporal, e := client.DialContext(startCtx, options)
	if e != nil {
		return e
	}
	defer temporal.Close()
	nc, e := nats.Connect(cfg.NATSURL, nats.Name("velin-"+mode), nats.Timeout(2*time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(time.Second), nats.RetryOnFailedConnect(true), nats.DisconnectErrHandler(func(_ *nats.Conn, _ error) { slog.Warn("progress bus disconnected", "event", "nats_disconnected") }), nats.ReconnectHandler(func(_ *nats.Conn) { slog.Info("progress bus connected", "event", "nats_connected") }))
	if e != nil {
		return e
	}
	defer nc.Close()
	var store platform.ObjectStore
	if endpoint := os.Getenv("AZURE_BLOB_ENDPOINT"); endpoint != "" {
		store, e = platform.NewAzureStore(endpoint, os.Getenv("AZURE_BLOB_CONTAINER"))
	} else {
		if cfg.Environment == "production" {
			return errors.New("production requires AZURE_BLOB_ENDPOINT")
		}
		store, e = platform.NewFileStore(cfg.ArtifactRoot)
	}
	if e != nil {
		return e
	}
	var handler http.Handler
	address := cfg.HTTPAddr
	if mode == "api" {
		auth, e := platform.NewAuthenticator(startCtx, cfg)
		if e != nil {
			return e
		}
		api := &platform.API{Config: cfg, Repo: repo, Temporal: temporal, NATS: nc, Store: store, Auth: auth, Limits: platform.NewRateLimiter()}
		handler = api.Handler()
	} else {
		activities := &platform.Activities{Repo: repo, Store: store, NATS: nc, CapabilitiesURL: cfg.CapabilitiesURL, CapabilitiesToken: cfg.CapabilitiesToken, HTTP: &http.Client{Timeout: 50 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		w := worker.New(temporal, cfg.TaskQueue, worker.Options{WorkerStopTimeout: 20 * time.Second, MaxConcurrentActivityExecutionSize: 8, MaxConcurrentWorkflowTaskExecutionSize: 16})
		platform.RegisterWorker(w, activities)
		if e = w.Start(); e != nil {
			return e
		}
		defer w.Stop()
		reportOpenWorkflows(startCtx, temporal, cfg.TemporalNamespace)
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refreshOpenWorkflows(ctx, temporal, cfg.TemporalNamespace, false)
				}
			}
		}()
		address = cfg.WorkerHTTPAddr
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"alive"}`))
		})
		mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
			healthCtx, done := context.WithTimeout(r.Context(), 3*time.Second)
			defer done()
			_, te := temporal.CheckHealth(healthCtx, &client.CheckHealthRequest{})
			req, _ := http.NewRequestWithContext(healthCtx, http.MethodGet, cfg.CapabilitiesURL+"/health/ready", nil)
			resp, capError := activities.HTTP.Do(req)
			capReady := capError == nil && resp.StatusCode == 200
			if resp != nil {
				_ = resp.Body.Close()
			}
			if repo.Pool.Ping(healthCtx) != nil || te != nil || !capReady {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ready":true}`))
		})
		mux.Handle("GET /metrics", platform.MetricsHandler())
		handler = mux
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	slog.Info("service started", "event", "service_started", "listen", address, "provider_mode", cfg.ProviderMode)
	select {
	case <-ctx.Done():
	case e = <-errorsCh:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if e = server.Shutdown(stopCtx); e != nil {
		_ = server.Close()
	}
	slog.Info("service stopping", "event", "service_stopping")
	return nil
}

// reportOpenWorkflows records how many commission workflows were already open
// when this worker started. Those executions resume from Temporal history; the
// count is an observation, not a claim of work done by this process.
func reportOpenWorkflows(ctx context.Context, c client.Client, namespace string) {
	refreshOpenWorkflows(ctx, c, namespace, true)
}

func refreshOpenWorkflows(ctx context.Context, c client.Client, namespace string, startup bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, e := c.CountWorkflow(ctx, &workflowservice.CountWorkflowExecutionsRequest{Namespace: namespace, Query: "WorkflowType='" + platform.WorkflowName + "' AND ExecutionStatus='Running'"})
	if e != nil {
		slog.Warn("open workflow count unavailable", "event", "open_workflows_unknown", "error_type", fmt.Sprintf("%T", e))
		platform.ActiveWorkflows.Set(math.NaN())
		return
	}
	platform.ActiveWorkflows.Set(float64(response.Count))
	if startup {
		platform.OpenAtStartup.Set(float64(response.Count))
		slog.Info("worker started with open workflows to resume", "event", "worker_resume_observed", "open_workflows", response.Count)
	}
}

func health(endpoint string) error {
	u, e := url.Parse(endpoint)
	if e != nil {
		return errors.New("invalid health URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Path != "/health/ready" {
		return errors.New("health probe requires loopback readiness URL")
	}
	c := http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, e := c.Get(endpoint)
	if e != nil {
		return errors.New("readiness probe connection failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return errors.New("readiness probe failed")
	}
	return nil
}
