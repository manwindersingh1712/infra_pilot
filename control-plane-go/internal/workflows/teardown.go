package workflows

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"infrapilot/internal/store"
)

// TeardownWorkflowID is unique per service, with REJECT_DUPLICATE in the
// starter: a second delete event for one service never starts a second run.
func TeardownWorkflowID(serviceID string) string { return "teardown-" + serviceID }

type TeardownInput struct {
	ServiceID string `json:"serviceId"`
	PurgeData bool   `json:"purgeData"`
}

// serviceIDPattern matches the cuids the API generates. The id goes into a
// container label filter, a volume name, a route file name and a host path, so
// nothing else may through (the starter and both activities check it).
var serviceIDPattern = regexp.MustCompile(`^[a-z0-9]{20,32}$`)

func checkServiceID(id string) error {
	if !serviceIDPattern.MatchString(id) {
		return temporal.NewNonRetryableApplicationError("invalid service id", "InvalidInput", nil)
	}
	return nil
}

// cancelOptions: CancelServiceDeploys waits for cancelled deploys to close and
// heartbeats while it does.
func cancelOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 6 * time.Minute, HeartbeatTimeout: time.Minute, RetryPolicy: retry,
	}
}

// TeardownWorkflow removes a deleted service's containers and route, and its
// data volume when PurgeData is set. The service row is already soft-deleted.
// First it stops the service's in-flight deploy workflows (a deploy past
// ClaimDeploy is not otherwise told about the delete), then it removes
// everything.
func TeardownWorkflow(ctx workflow.Context, in TeardownInput) error {
	var a *Activities
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, cancelOptions()), a.CancelServiceDeploys, in).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, shortOptions()), a.TeardownService, in).Get(ctx, nil)
}

// CancelServiceDeploys requests cancellation of the DeployWorkflow of every
// queued, building or deploying deployment of the service and waits until they
// have closed (their cleanup removes what they started). A workflow that does
// not exist yet or is gone is fine: a late one stops at LoadJob, which refuses
// deleted services. If one is still running after CancelWaitTimeout the
// activity fails (retryable) rather than let teardown race it.
func (a *Activities) CancelServiceDeploys(ctx context.Context, in TeardownInput) error {
	if err := checkServiceID(in.ServiceID); err != nil {
		return err
	}
	ids, err := a.Store.ListOpenDeploymentIDs(ctx, in.ServiceID)
	if err != nil {
		return err
	}
	poll, wait := a.Cfg.CancelPollInterval, a.Cfg.CancelWaitTimeout
	if poll <= 0 {
		poll = time.Second
	}
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for _, id := range ids {
		wfID := DeployWorkflowID(id)
		if err := a.Temporal.CancelWorkflow(ctx, wfID, ""); err != nil {
			var nf *serviceerror.NotFound
			if errors.As(err, &nf) {
				continue
			}
			return fmt.Errorf("cancel %s: %w", wfID, err)
		}
		for {
			activity.RecordHeartbeat(ctx, wfID)
			resp, err := a.Temporal.DescribeWorkflowExecution(ctx, wfID, "")
			var nf *serviceerror.NotFound
			if errors.As(err, &nf) {
				break
			}
			if err != nil {
				return fmt.Errorf("describe %s: %w", wfID, err)
			}
			if resp.GetWorkflowExecutionInfo().GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("deploy workflow %s still running after %s", wfID, wait)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(poll):
			}
		}
	}
	return nil
}

// TeardownService is idempotent: retries remove what is left, and a service
// that is already torn down succeeds. It refuses a service that is not
// soft-deleted, so a stray event can never remove a live service. The volume
// goes only for an exact PurgeData == true, and only the volume named after
// this (validated) service id.
func (a *Activities) TeardownService(ctx context.Context, in TeardownInput) error {
	if err := checkServiceID(in.ServiceID); err != nil {
		return err
	}
	svc, err := a.Store.GetServiceAnyState(ctx, in.ServiceID)
	if errors.Is(err, store.ErrNotFound) {
		a.Log.Warn("teardown: service does not exist; nothing to do", "serviceId", in.ServiceID)
		return nil
	}
	if err != nil {
		return err
	}
	if svc.DeletedAt == nil {
		return temporal.NewNonRetryableApplicationError("service is not deleted", "NotDeleted", nil)
	}

	// Deployments still queued/building/deploying will never run now.
	if _, err := a.Store.FailOpenDeployments(ctx, in.ServiceID); err != nil {
		return err
	}
	live, err := a.Store.ListLiveDeployments(ctx, in.ServiceID, "")
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(live))
	for _, d := range live {
		if err := a.Docker.Remove(ctx, ContainerName(d.ID)); err != nil {
			return err
		}
		ids = append(ids, d.ID)
	}
	// Containers no row points at (a cancelled deploy that had started one).
	if err := a.Docker.RemoveServiceContainers(ctx, in.ServiceID); err != nil {
		return err
	}
	if err := a.Store.MarkStopped(ctx, ids); err != nil {
		return err
	}
	if err := a.Router.RemoveRoute(ctx, in.ServiceID); err != nil {
		return err
	}
	if in.PurgeData {
		if err := a.Docker.RemoveVolume(ctx, VolumeName(in.ServiceID)); err != nil {
			return err
		}
		if err := a.removeLegacyData(in.ServiceID); err != nil {
			return err
		}
	}
	return a.Store.ScrubDeletedService(ctx, in.ServiceID)
}

// removeLegacyData deletes the Node-era host directory <LegacyVolumeRoot>/<serviceId>
// (the only place EnsureVolume copies legacy data from by default). It goes
// through os.Root, so a symlink at that name is removed as a link and never
// followed out of the root. A missing root or directory is fine.
func (a *Activities) removeLegacyData(serviceID string) error {
	if a.Cfg.LegacyVolumeRoot == "" {
		return nil
	}
	root, err := os.OpenRoot(a.Cfg.LegacyVolumeRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.RemoveAll(serviceID); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove legacy data: %w", err)
	}
	return nil
}
