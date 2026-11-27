package protectedcore

import "testing"

func TestComponentsContainRequiredProtectedCore(t *testing.T) {
	required := map[string]bool{
		"auth":                    false,
		"policy":                  false,
		"approval":                false,
		"updater":                 false,
		"rescue":                  false,
		"privileged-broker":       false,
		"protected-core-manifest": false,
	}
	seen := map[string]struct{}{}
	for _, item := range Components() {
		if _, exists := seen[item.Name]; exists {
			t.Fatalf("duplicate component %q", item.Name)
		}
		seen[item.Name] = struct{}{}
		if _, ok := required[item.Name]; ok {
			required[item.Name] = true
		}
	}
	for name, ok := range required {
		if !ok {
			t.Fatalf("required protected-core component missing: %s", name)
		}
	}
}

func TestProtectedPathClassification(t *testing.T) {
	for _, path := range []string{
		"/etc/mcpshka/auth.json",
		"/etc/mcpshka/mcp-auth-header",
		"/usr/local/bin/mcpshka",
		"/etc/systemd/system/mcpshka.service",
		"/usr/local/libexec/mcpshka-broker",
		"/usr/local/libexec/mcpshka-install-broker",
		"/usr/local/libexec/mcpshka-codex-bridge.py",
		"/usr/local/libexec/mcpshka-self-deploy",
		"/usr/local/libexec/mcpshka-release-probe.py",
		"/usr/local/libexec/mcpshka-state-store.py",
		"/usr/local/libexec/mcpshka-migrate-service-user",
		"/usr/local/libexec/mcpshka-migrate-auth-registry.py",
		"/usr/local/libexec/mcpshka-user.service",
		"/var/lib/mcpshka-executor/state.db",
		"/usr/local/libexec/diag-traffic-route-observe",
		"/var/lib/diag/traffic-runs",
		"/etc/mcpshka/broker.json",
	} {
		if !RuntimePathProtected(path) {
			t.Fatalf("runtime path not protected: %s", path)
		}
	}
	if RuntimePathProtected("/root/repos/mcpshka/README.md") {
		t.Fatal("normal repository path unexpectedly protected as runtime")
	}

	for _, path := range []string{
		"internal/authn/authn.go",
		"internal/policy/policy.go",
		"internal/approval/store.go",
		"internal/deploy/deploy.go",
		"internal/rootbroker/rootbroker.go",
		"internal/hostevents/events.go",
		"internal/brokerdocker/validate.go",
		"internal/tools/cloud/cloud_dispatch.go",
		"internal/tools/knowledgetools/local.go",
		"internal/tools/ownerautomation/browser_tools.go",
		"internal/tools/networktools/network.go",
		"internal/tools/orchestration/agents.go",
		"internal/tools/forgeops/forge.go",
		"internal/tools/releaseops/release.go",
		"internal/tools/taskplan/plan.go",
		"internal/tools/adminops/session.go",
		"internal/tools/hostops/tools_service.go",
		"internal/tools/fstools/fs.go",
		"internal/tools/projectops/project.go",
		"internal/tools/capabilities/resolve.go",
		"internal/tools/integration/integration_worker.go",
		"internal/tools/issues/issues_tools.go",
		"internal/tools/forge/ci.go",
		"internal/tools/recovery/diagnosis.go",
		"internal/tools/workyield/yield.go",
		"internal/tools/health/blockers.go",
		"internal/tools/roottask/task.go",
		"internal/executor/worker.go",
		"internal/projectenv/spec.go",
		"internal/gitobjects/objects.go",
		"internal/cievidence/cievidence.go",
		"internal/brokerclient/client.go",
		"packaging/native/install-broker.sh",
		"packaging/native/release-probe.py",
		"packaging/native/rollback.sh",
		"packaging/native/state-store.py",
		"packaging/native/migrate-service-user.sh",
		"packaging/native/migrate-auth-registry.py",
		"packaging/native/codex-bridge.py",
		"internal/rootbroker/diagtraffic/worker.go",
		"internal/protectedcore/protectedcore.go",
	} {
		if !SourcePathProtected(path) {
			t.Fatalf("source path not protected: %s", path)
		}
	}
	if SourcePathProtected("internal/dockerops/docker.go") {
		t.Fatal("normal source path unexpectedly protected")
	}
}
