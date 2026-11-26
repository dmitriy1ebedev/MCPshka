package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/approval"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/deploy"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/gitops"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/networkops"
	"mcpshka/internal/project"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/verify"
	"mcpshka/internal/workflow"
)

// serverDeps carries the shared services built by newServerWithCoordinator to
// the per-domain tool registration files (tools_*.go).
type serverDeps struct {
	manager          *jobs.Manager
	files            *filesystem.Service
	artifactManager  *artifacts.Manager
	git              *gitops.Service
	projects         *project.Service
	profiles         *projectprofile.Service
	deploys          *deploy.Service
	verifier         *verify.Service
	leases           *projectlease.Service
	pipelines        *workflow.Engine
	guard            func(string, string) error
	idempotencyStore *idempotency.Store
	systemdServices  *systemdsvc.Service
	dockerService    *dockerops.Service
	networkService   *networkops.Service
	controlService   *controlplane.Service
	laneService      *laneprovider.Service
	approvalStore    *approval.Store
	healthCollector  *healthlayer.Collector
	inbox            *artifacts.Inbox
	integrations     *integrationqueue.Runtime
	c                *workflowCoordinator
	s                *mcp.Server
}

// makeHostInventory starts housekeeping with evidence only: no filenames or
// ages authorize deletion.
func (d *serverDeps) makeHostInventory(ctx context.Context, path string) (systeminfo.HostStorageInventory, error) {
	result, err := systeminfo.InventoryHostStorage(ctx, d.manager.Policy(), path)
	if err != nil {
		return result, err
	}
	if status, e := d.git.Status(path, 8192); e == nil {
		if status.Truncated {
			result.Git = "unknown: Git status truncated"
		} else if status.Dirty {
			result.Git = "known: uncommitted changes"
		} else {
			result.Git = "known: clean"
		}
	}
	if lease, e := d.leases.Status(path); e == nil {
		if lease.Held {
			result.Leases = "known: writer lease active"
		} else if lease.ReconciliationRequired {
			result.Leases = "unknown: owner reconciliation required"
		} else {
			result.Leases = "known: no writer lease"
		}
	}
	activity := d.manager.ProjectActivity(path, 64)
	if activity.Truncated || activity.UnknownCount > 0 {
		result.ActiveJobs = fmt.Sprintf("unknown: %d active/uncertain jobs, %d unknown, truncated=%t", activity.Count, activity.UnknownCount, activity.Truncated)
	} else {
		result.ActiveJobs = fmt.Sprintf("known: %d tracked active jobs", activity.Count)
	}
	return result, nil
}
