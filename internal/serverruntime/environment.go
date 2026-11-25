package serverruntime

import (
	"encoding/json"
	"fmt"
	"mcpshka/internal/audit"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/verify"
	"mcpshka/internal/workbenchruntime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func NewFromEnvironment() (*jobs.Manager, *filesystem.Service, func(), error) {
	if _, err := selfheal.FromEnvironment(); err != nil {
		return nil, nil, nil, err
	}
	stateDir := os.Getenv("MCPSHKA_STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/mcpshka/jobs"
	}
	auditPath := os.Getenv("MCPSHKA_AUDIT_PATH")
	if auditPath == "" {
		auditPath = filepath.Join(filepath.Dir(stateDir), "audit.jsonl")
	}
	logger, err := audit.New(auditPath, 2*1024*1024)
	if err != nil {
		return nil, nil, nil, err
	}
	p := policy.Default()
	if profile := strings.TrimSpace(os.Getenv("MCPSHKA_DEFAULT_ROLE_PROFILE")); profile != "" {
		if err := p.SetDefaultProfile(profile); err != nil {
			return nil, nil, nil, fmt.Errorf("MCPSHKA_DEFAULT_ROLE_PROFILE: %w", err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MCPSHKA_PROJECT_PROFILES")); raw != "" {
		var overrides map[string]string
		if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
			return nil, nil, nil, fmt.Errorf("MCPSHKA_PROJECT_PROFILES: %w", err)
		}
		for root, profile := range overrides {
			if err := p.SetProjectProfile(root, profile); err != nil {
				return nil, nil, nil, fmt.Errorf("MCPSHKA_PROJECT_PROFILES %q: %w", root, err)
			}
		}
	}
	manager, err := jobs.NewWithAudit(stateDir, p, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	manager.UseHostHeavyQueue()
	// Project tests run only through the sealed root Workbench recipe.
	// Registered profiles let any onboarded repository run its sealed test.
	projectprofile.SetRegistry(manager.Database())
	manager.SetExternalRuntime(workbenchruntime.New(p))
	scopeMode := strings.ToLower(strings.TrimSpace(os.Getenv("MCPSHKA_JOB_SCOPES")))
	if scopeMode != "0" && scopeMode != "false" && scopeMode != "off" {
		if err := manager.EnableSystemdScopes(); err != nil {
			return nil, nil, nil, fmt.Errorf("enable per-job systemd scopes: %w", err)
		}
	} else {
		// P5.3: heavy jobs leave the server's own CPU share (Delegate=yes).
		heavy, err := manager.EnableDelegatedCgroup()
		result := "heavy jobs in " + heavy
		if err != nil {
			result = "not delegated, heavy jobs share the service cgroup: " + err.Error()
		}
		_ = logger.Log(audit.Event{Type: "job_cgroup", Result: result})
	}
	verify.HeavyPlacement = func() (bool, string) {
		if manager.SystemdScopesEnabled() {
			return true, "per-job systemd scopes"
		}
		if heavy := manager.HeavyCgroup(); heavy != "" {
			return true, "heavy jobs in " + heavy
		}
		return false, "heavy jobs share the server cgroup: Delegate=yes takes effect at the next mcpshka.service start"
	}
	files := filesystem.NewWithAudit(p, logger)
	stopReaper := manager.StartReaper(10*time.Minute, 24*time.Hour)
	stopPruner := func() {}
	if db := manager.Database(); db != nil {
		stopPruner = idempotency.StartPruner(db, 10*time.Minute, func(n int, err error) {
			result := fmt.Sprintf("%d expired receipts removed", n)
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{Type: "idempotency_pruned", Result: result})
		})
	}
	stop := func() {
		stopReaper()
		stopPruner()
		if err := manager.Close(5 * time.Second); err != nil {
			_ = logger.Log(audit.Event{Type: "manager_shutdown_error", Result: err.Error()})
		}
	}
	return manager, files, stop, nil
}
