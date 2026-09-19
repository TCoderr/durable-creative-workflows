package platform

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"
)

// The dev-server tests run a real Temporal server (the SDK downloads the CLI
// once) and are enabled with VELIN_TEMPORAL_DEV=1. They prove that a workflow
// interrupted by a worker crash resumes on another worker from history, that
// the recorded history replays deterministically, and they capture that
// history as the committed replay fixture when VELIN_RECORD_HISTORY=1.

const devQueue = "velin-commissions-test"

// devIdentity keeps recorded histories free of the machine hostname the SDK
// would otherwise embed as the client and worker identity.
const devIdentity = "velin-test-worker"

// gatedRecorder can hold the strategy step open for as long as a test wants.
// A crashed worker never reports failure, so the blocked attempt ignores
// cancellation and simply stops heartbeating when the test "kills" its worker.
type gatedRecorder struct {
	*recorder
	block     atomic.Bool
	heartbeat atomic.Bool
	release   chan struct{}
	started   chan struct{}
	attempts  sync.Map
}

func newGatedRecorder() *gatedRecorder {
	g := &gatedRecorder{started: make(chan struct{}, 8), release: make(chan struct{})}
	g.heartbeat.Store(true)
	g.recorder = newRecorder(func(ctx context.Context, in StepInput, _ string) (StepResult, error) {
		count, _ := g.attempts.LoadOrStore(in.StepID, new(atomic.Int32))
		count.(*atomic.Int32).Add(1)
		if in.Capability == "strategy" && g.block.Load() {
			select {
			case g.started <- struct{}{}:
			default:
			}
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-g.release:
					return fixture(in), nil
				case <-ticker.C:
					if g.heartbeat.Load() {
						activity.RecordHeartbeat(ctx, "blocked")
					}
				}
			}
		}
		return fixture(in), nil
	})
	return g
}
func (g *gatedRecorder) releaseAll() {
	select {
	case <-g.release:
	default:
		close(g.release)
	}
}
func (g *gatedRecorder) attemptCount(stepID string) int32 {
	count, ok := g.attempts.Load(stepID)
	if !ok {
		return 0
	}
	return count.(*atomic.Int32).Load()
}

func registerDevWorker(w worker.Worker, r *recorder) {
	w.RegisterWorkflowWithOptions(DurableCommission, workflowRegisterOptions())
	w.RegisterActivityWithOptions(r.RecordRun, activity.RegisterOptions{Name: RunActivity})
	w.RegisterActivityWithOptions(r.RunCapability, activity.RegisterOptions{Name: StepActivity})
	w.RegisterActivityWithOptions(r.RecordEvent, activity.RegisterOptions{Name: EventActivity})
	w.RegisterActivityWithOptions(r.RecordRevision, activity.RegisterOptions{Name: RevisionActivity})
	w.RegisterActivityWithOptions(r.RecordApprovalRequest, activity.RegisterOptions{Name: ApprovalActivity})
	w.RegisterActivityWithOptions(r.RecordDecision, activity.RegisterOptions{Name: DecisionActivity})
	w.RegisterActivityWithOptions(r.ProduceArtifact, activity.RegisterOptions{Name: ProductionActivity})
}

func devServer(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("VELIN_TEMPORAL_DEV") != "1" {
		t.Skip("set VELIN_TEMPORAL_DEV=1 to run tests against a real Temporal dev server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	options := testsuite.DevServerOptions{ClientOptions: &client.Options{Identity: devIdentity}, LogLevel: "warn"}
	if path := os.Getenv("VELIN_TEMPORAL_CLI"); path != "" {
		options.ExistingPath = path
	}
	server, e := testsuite.StartDevServer(ctx, options)
	require.NoError(t, e)
	t.Cleanup(func() { _ = server.Stop() })
	return server.Client()
}

func waitForStage(t *testing.T, c client.Client, id string, stage string, timeout time.Duration) WorkflowState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var state WorkflowState
	for time.Now().Before(deadline) {
		value, e := c.QueryWorkflow(context.Background(), id, "", StateQuery)
		if e == nil && value.Get(&state) == nil && state.Stage == stage {
			return state
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.Failf(t, "stage not reached", "wanted %s, last %+v", stage, state)
	return state
}

func TestDevServerWorkflowResumesAfterWorkerCrashAndHistoryReplays(t *testing.T) {
	c := devServer(t)
	ctx := context.Background()
	crashed := newGatedRecorder()
	crashed.block.Store(true)
	t.Cleanup(crashed.releaseAll)
	first := worker.New(c, devQueue, worker.Options{WorkerStopTimeout: time.Second, Identity: devIdentity})
	registerDevWorker(first, crashed.recorder)
	require.NoError(t, first.Start())
	in := input()
	in.Commission.ID = "6b3c9e5a-2f1d-4c8b-9a7e-0d1c2b3a4f5e"
	run, e := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: in.Commission.ID, TaskQueue: devQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, WorkflowName, in)
	require.NoError(t, e)
	select {
	case <-crashed.started:
	case <-time.After(60 * time.Second):
		require.Fail(t, "the strategy step never started on the first worker")
	}
	stateBeforeCrash := waitForStage(t, c, in.Commission.ID, StageStrategy, 10*time.Second)
	require.Equal(t, 2, stateBeforeCrash.StepsCompleted)

	// Kill the worker while the strategy step is in flight. The attempt stops
	// heartbeating and never reports; Temporal keeps the history, times the
	// attempt out on heartbeat and retries it on whichever worker is alive.
	crashed.heartbeat.Store(false)
	first.Stop()

	healthy := newGatedRecorder()
	second := worker.New(c, devQueue, worker.Options{WorkerStopTimeout: time.Second, Identity: devIdentity})
	registerDevWorker(second, healthy.recorder)
	require.NoError(t, second.Start())
	defer second.Stop()
	waiting := waitForStage(t, c, in.Commission.ID, StageWaiting, 90*time.Second)
	require.Equal(t, stateBeforeCrash.RunID, waiting.RunID, "resumption stays inside the same run")
	require.Equal(t, RequiredStepCount, waiting.StepsCompleted)
	require.NotNil(t, waiting.PendingApproval)
	require.EqualValues(t, 1, healthy.attemptCount(StepID(in.Commission.ID, "strategy", 0)), "the second worker ran the interrupted step once")
	require.EqualValues(t, 0, healthy.attemptCount(StepID(in.Commission.ID, "brief", 0)), "completed steps are not repeated after the crash")

	require.NoError(t, c.SignalWorkflow(ctx, in.Commission.ID, "", DecisionSignal, decisionFor(*waiting.PendingApproval, "approve-after-crash", ActionApprove)))
	var final WorkflowState
	require.NoError(t, run.Get(ctx, &final))
	require.Equal(t, StageCompleted, final.Stage)
	require.EqualValues(t, 1, healthy.produced.Load())
	require.EqualValues(t, 1, healthy.decided.Load())

	// Deterministic replay of the actual recorded history against the current
	// workflow code, then optionally capture it as the committed fixture.
	history := &historypb.History{}
	iterator := c.GetWorkflowHistory(ctx, in.Commission.ID, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, e := iterator.Next()
		require.NoError(t, e)
		history.Events = append(history.Events, event)
	}
	require.Greater(t, len(history.Events), 40)
	// Retried attempts are internal to the activity; the started event that
	// finally completes carries the attempt number of the successful retry.
	retried := false
	for _, event := range history.Events {
		if attributes := event.GetActivityTaskStartedEventAttributes(); attributes != nil && attributes.GetAttempt() >= 2 {
			retried = true
		}
	}
	require.True(t, retried, "history records the interrupted step completing on a later attempt")
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(DurableCommission, workflowRegisterOptions())
	require.NoError(t, replayer.ReplayWorkflowHistory(nil, history))
	if os.Getenv("VELIN_RECORD_HISTORY") == "1" {
		data, e := protojson.MarshalOptions{Indent: "  "}.Marshal(history)
		require.NoError(t, e)
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(filepath.Join("testdata", "durable-commission-resume.json"), data, 0o644))
	}
}

func TestDevServerCancellationIsExplicitTerminalState(t *testing.T) {
	c := devServer(t)
	ctx := context.Background()
	r := newGatedRecorder()
	r.block.Store(true)
	w := worker.New(c, devQueue, worker.Options{WorkerStopTimeout: time.Second, Identity: devIdentity})
	registerDevWorker(w, r.recorder)
	require.NoError(t, w.Start())
	t.Cleanup(func() {
		r.releaseAll()
		w.Stop()
	})
	in := input()
	in.Commission.ID = "7c4d0f6b-3a2e-4d9c-8b8f-1e2d3c4b5a6f"
	run, e := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: in.Commission.ID, TaskQueue: devQueue}, WorkflowName, in)
	require.NoError(t, e)
	<-r.started
	require.NoError(t, c.CancelWorkflow(ctx, in.Commission.ID, ""))
	var final WorkflowState
	require.Error(t, run.Get(ctx, &final))
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if typ, _ := r.terminal(); typ == "workflow.cancelled" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	typ, code := r.terminal()
	require.Equal(t, "workflow.cancelled", typ)
	require.Equal(t, "WORKFLOW_CANCELLED", code)
}

// TestReplayRecordedHistory replays the committed fixture without any server.
// It fails when a change to the workflow breaks determinism against history
// produced by the previous version.
func TestReplayRecordedHistory(t *testing.T) {
	path := filepath.Join("testdata", "durable-commission-resume.json")
	_, e := os.Stat(path)
	require.NoError(t, e, "the committed replay fixture is a required validation gate")
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(DurableCommission, workflowRegisterOptions())
	require.NoError(t, replayer.ReplayWorkflowHistoryFromJSONFile(nil, path))
}
