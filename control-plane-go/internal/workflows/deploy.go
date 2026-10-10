package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"infrapilot/internal/store"
)

// retry matches the Node worker: 1 attempt + 5 retries with backoff.
var retry = &temporal.RetryPolicy{
	InitialInterval:    10 * time.Second,
	BackoffCoefficient: 2,
	MaximumInterval:    5 * time.Minute,
	MaximumAttempts:    6,
}

func shortOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Minute, RetryPolicy: retry}
}

func buildOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Minute, ScheduleToCloseTimeout: 90 * time.Minute,
		HeartbeatTimeout: time.Minute, RetryPolicy: retry}
}

// runOptions: RunContainer pulls an image and starts a container, and
// heartbeats through keepAlive, so it gets its own timeouts and 3 attempts.
func runOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 10 * time.Second, BackoffCoefficient: 2, MaximumInterval: 5 * time.Minute, MaximumAttempts: 3,
		},
	}
}

// healthStartToClose is fixed. The probe deadline (HEALTH_TIMEOUT_SECONDS) is
// capped at 600 s in internal/config, so this always exceeds it, with room for
// a first image pull. A constant keeps workflow code free of mutable state.
const healthStartToClose = 11 * time.Minute

// healthOptions allows one attempt: an unhealthy container fails the deploy
// and the previous deployment keeps serving.
func healthOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: healthStartToClose, HeartbeatTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	}
}

// stage records how far a deploy got, so cleanup never undoes live traffic.
type stage struct{ routeSwitched, deployed bool }

// DeployWorkflow builds (if needed) and runs one deployment. Its ID is
// DeployWorkflowID(deploymentID), so starting it twice is harmless.
func DeployWorkflow(ctx workflow.Context, deploymentID string) error {
	var a *Activities
	short := workflow.WithActivityOptions(ctx, shortOptions())

	var job store.Job
	if err := workflow.ExecuteActivity(short, a.LoadJob, deploymentID).Get(ctx, &job); err != nil {
		return err
	}
	if job.Status != store.StatusQueued {
		return nil // already handled by an earlier run or by the Node worker
	}
	job.RepoURL = nil // may embed a token; never pass it to another activity

	st, err := deploy(ctx, job)
	if err == nil {
		return nil
	}
	log := workflow.GetLogger(ctx)
	if st.deployed {
		// The deployment is live. Keep it, fail the run so it shows in the UI.
		log.Warn("post-deploy step failed; deployment kept", "deploymentId", deploymentID)
		return err
	}
	// Record the failure even if the workflow is being cancelled. MarkFailed
	// never overwrites a deployed row.
	cleanup, _ := workflow.NewDisconnectedContext(ctx)
	cleanup = workflow.WithActivityOptions(cleanup, shortOptions())
	if cerr := workflow.ExecuteActivity(cleanup, a.MarkFailed, deploymentID).Get(cleanup, nil); cerr != nil {
		log.Warn("MarkFailed failed", "deploymentId", deploymentID)
	}
	// After SwitchRoute the new container is serving: keep it.
	if !st.routeSwitched {
		if cerr := workflow.ExecuteActivity(cleanup, a.RemoveContainer, ContainerName(deploymentID)).Get(cleanup, nil); cerr != nil {
			log.Warn("RemoveContainer failed", "deploymentId", deploymentID)
		}
	}
	return err
}

func deploy(ctx workflow.Context, job store.Job) (stage, error) {
	var a *Activities
	var st stage
	short := workflow.WithActivityOptions(ctx, shortOptions())
	managed := store.IsManaged(job.ServiceType)

	claim := func(from, to string) (bool, error) {
		var won bool
		err := workflow.ExecuteActivity(short, a.Claim, ClaimInput{DeploymentID: job.DeploymentID, From: from, To: to}).Get(ctx, &won)
		return won, err
	}

	if !managed && job.Image == nil {
		if won, err := claim(store.StatusQueued, store.StatusBuilding); err != nil || !won {
			return st, err
		}
		var image string
		build := BuildInput{DeploymentID: job.DeploymentID}
		if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, buildOptions()), a.BuildImage, build).Get(ctx, &image); err != nil {
			return st, err
		}
		if err := workflow.ExecuteActivity(short, a.MarkBuilt, MarkBuiltInput{DeploymentID: job.DeploymentID, Image: image}).Get(ctx, nil); err != nil {
			return st, err
		}
		job.Image = &image
	}
	if won, err := claim(store.StatusQueued, store.StatusDeploying); err != nil || !won {
		return st, err
	}

	var network string
	if err := workflow.ExecuteActivity(short, a.EnsureNetwork, job.ProjectID).Get(ctx, &network); err != nil {
		return st, err
	}
	var volume string
	if managed {
		if err := workflow.ExecuteActivity(short, a.EnsureCredentials, job).Get(ctx, nil); err != nil {
			return st, err
		}
		if err := workflow.ExecuteActivity(short, a.EnsureVolume, job).Get(ctx, &volume); err != nil {
			return st, err
		}
		// Two database processes must never share a data directory.
		retire := RetireInput{ServiceID: job.ServiceID, KeepDeploymentID: job.DeploymentID}
		if err := workflow.ExecuteActivity(short, a.RetirePrevious, retire).Get(ctx, nil); err != nil {
			return st, err
		}
	}

	var run RunResult
	runCtx := workflow.WithActivityOptions(ctx, runOptions())
	if err := workflow.ExecuteActivity(runCtx, a.RunContainer, RunInput{Job: job, Network: network, Volume: volume}).Get(ctx, &run); err != nil {
		return st, err
	}
	wait := WaitInput{Network: network, Host: run.ContainerName, Port: run.ContainerPort}
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, healthOptions()), a.WaitHealthy, wait).Get(ctx, nil); err != nil {
		return st, err
	}

	var runtimeURL *string
	if !managed {
		var url string
		route := RouteInput{ServiceID: job.ServiceID, ContainerName: run.ContainerName, Port: run.ContainerPort}
		if err := workflow.ExecuteActivity(short, a.SwitchRoute, route).Get(ctx, &url); err != nil {
			return st, err
		}
		st.routeSwitched = true
		runtimeURL = &url
	}
	var volumePath *string
	if volume != "" {
		volumePath = &volume
	}
	deployed := DeployedInput{
		DeploymentID: job.DeploymentID, ContainerID: run.ContainerID, HostPort: run.HostPort,
		ContainerPort: run.ContainerPort, RuntimeURL: runtimeURL, VolumePath: volumePath,
	}
	if err := workflow.ExecuteActivity(short, a.MarkDeployed, deployed).Get(ctx, nil); err != nil {
		return st, err
	}
	st.deployed = true

	if !managed {
		retire := RetireInput{ServiceID: job.ServiceID, KeepDeploymentID: job.DeploymentID}
		if err := workflow.ExecuteActivity(short, a.RetirePrevious, retire).Get(ctx, nil); err != nil {
			return st, fmt.Errorf("post-deploy %s: %w", "retire", err)
		}
	}
	stream := StreamInput{DeploymentID: job.DeploymentID, ContainerID: run.ContainerID}
	if err := workflow.ExecuteActivity(short, a.StartLogStream, stream).Get(ctx, nil); err != nil {
		return st, fmt.Errorf("post-deploy %s: %w", "logs", err)
	}
	return st, nil
}
