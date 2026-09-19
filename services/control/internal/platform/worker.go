package platform

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// RegisterWorker binds the workflow and every activity under their stable
// names. Names are part of the durable contract: a recorded history refers to
// them, so renaming one breaks replay.
func RegisterWorker(w worker.Registry, a *Activities) {
	w.RegisterWorkflowWithOptions(DurableCommission, workflow.RegisterOptions{Name: WorkflowName})
	w.RegisterActivityWithOptions(a.RecordRun, activity.RegisterOptions{Name: RunActivity})
	w.RegisterActivityWithOptions(a.RunCapability, activity.RegisterOptions{Name: StepActivity})
	w.RegisterActivityWithOptions(a.RecordEvent, activity.RegisterOptions{Name: EventActivity})
	w.RegisterActivityWithOptions(a.RecordRevision, activity.RegisterOptions{Name: RevisionActivity})
	w.RegisterActivityWithOptions(a.RecordApprovalRequest, activity.RegisterOptions{Name: ApprovalActivity})
	w.RegisterActivityWithOptions(a.RecordDecision, activity.RegisterOptions{Name: DecisionActivity})
	w.RegisterActivityWithOptions(a.ProduceArtifact, activity.RegisterOptions{Name: ProductionActivity})
}
