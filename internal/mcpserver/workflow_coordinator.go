package mcpserver

import (
	"context"
	"errors"
	"mcpshka/internal/deploy"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/mission"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/tools/approvedtask"
	"mcpshka/internal/workflow"
)

// One configured coordinator belongs to one daemon lifetime. API registration,
// job admission and background maintenance share its exact engines; maintenance
// must not reconstruct a default engine and discard configured authority.
type workflowCoordinator struct {
	indexRepairer *knowledgeops.IndexRepairer
	selfHeal      *selfheal.Service
	dockerService *dockerops.Service
	manager       *jobs.Manager
	leases        *projectlease.Service
	deploys       *deploy.Service
	pipelines     *workflow.Engine
	integrations  *integrationqueue.Runtime
	missions      *mission.Controller
}

func newWorkflowCoordinator(manager *jobs.Manager) *workflowCoordinator {
	leases, deploys := managedServices(manager)
	c := &workflowCoordinator{manager: manager, leases: leases, deploys: deploys, pipelines: workflow.New(manager), integrations: integrationqueue.NewRuntime(manager.Database(), manager.Policy())}
	cfg, err := selfheal.FromEnvironment()
	// Production rejects invalid configuration before starting any services.
	if err != nil {
		cfg.Enabled = false
	}
	c.dockerService = dockerops.NewFromEnvironment()
	c.selfHeal = selfheal.New(manager.Database(), c.dockerService, manager.AuditLogger(), cfg)
	c.indexRepairer = knowledgeops.NewIndexRepairer(manager, c.selfHeal, heavyqueue.HostReady)
	c.pipelines.Admission = func(root string) error {
		pending, err := deploys.Pending()
		if err != nil {
			return err
		}
		if pending {
			return errors.New("deployment pending: HOLD")
		}
		status, err := leases.Status(root)
		if err != nil {
			return err
		}
		if status.Held {
			return errors.New("project lease already held: HOLD")
		}
		return nil
	}
	manager.SetLeaseGuard(c.Guard)
	manager.SetHolderGuard(c.HolderGuard)
	manager.SetHolderKeepalive(c.HolderKeepalive)
	manager.SetReapReferences(c.leases.ReferencedJobs)
	return c
}

func (c *workflowCoordinator) Guard(path, id string) error {
	if err := c.pipelines.Guard(path, id); err != nil {
		return err
	}
	return c.leases.Guard(path, id)
}

// HolderGuard keeps workflow fencing; only the interactive lease check is
// replaced by proof of the exact current holder token.
func (c *workflowCoordinator) HolderGuard(path, token string) error {
	if err := c.pipelines.Guard(path, ""); err != nil {
		return err
	}
	err := c.leases.GuardHolder(path, token)
	// Presenting the secret holder token is itself proof the holder is alive:
	// refresh a stale heartbeat and admit, instead of sending the AI to call
	// project_lease_heartbeat by hand (ChatGPT DIAG audit 03.11.2026).
	var fe *failure.Error
	if errors.As(err, &fe) && fe.Code == "LEASE_HEARTBEAT_STALE" {
		if _, hbErr := c.leases.HeartbeatRefresh(path, token); hbErr == nil {
			err = c.leases.GuardHolder(path, token)
		}
	}
	return err
}

// HolderKeepalive renews the holder's lease while a job it started runs.
func (c *workflowCoordinator) HolderKeepalive(path, token string) error {
	_, err := c.leases.HeartbeatRefresh(path, token)
	return err
}

func (c *workflowCoordinator) Maintain() error {
	c.leases.MaintainManaged(c.manager, c.deploys)
	var missionErr error
	if c.missions != nil {
		missionErr = c.missions.Maintain()
	}
	nativeErr := errors.Join(c.pipelines.Maintain(), missionErr, approvedtask.Maintain())
	if nativeErr != nil {
		return nativeErr
	}
	_, indexErr := c.indexRepairer.Tick(context.Background())
	return indexErr
}
