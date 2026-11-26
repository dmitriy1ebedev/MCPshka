package transientheal

import (
	"strings"
	"testing"
)

func TestResetFailedTransientOwnership(t *testing.T) {
	p := map[string]string{"ActiveState": "failed", "Transient": "yes", "MainPID": "0", "ControlPID": "0", "ExecStart": "{ path=/usr/local/libexec/mcpshka-broker ; argv[]=... ; }"}
	for _, unit := range []string{"mcpshka-project-release-diag-0164.service", "project-release-diag-0164.service", "run-abc123.service", "run-rabc123.service"} {
		if !ownFailedTransient(unit, p, nil) {
			t.Fatal(unit)
		}
	}
	for _, unit := range []string{"laned.service", "xray.service", "run-../../.service", "docker-123.scope"} {
		if ownFailedTransient(unit, p, nil) {
			t.Fatal(unit)
		}
	}
	p["MainPID"] = "42"
	if ownFailedTransient("run-abc.service", p, nil) {
		t.Fatal("active process")
	}
	p["MainPID"] = "0"
	p["ExecStart"] = "{ path=/bin/sh ; argv[]=mcpshka ; }"
	if ownFailedTransient("run-abc.service", p, nil) {
		t.Fatal("foreign command")
	}
}

func TestResetFailedDockerScopeRequiresOwnedIdleScope(t *testing.T) {
	unit := "docker-" + strings.Repeat("a", 64) + ".scope"
	props := map[string]string{"ActiveState": "failed", "Transient": "yes", "ControlGroup": "", "TasksCurrent": "0"}
	if ownFailedTransient(unit, props, nil) {
		t.Fatal("foreign container")
	}
	if !ownFailedTransient(unit, props, map[string]bool{unit: true}) {
		t.Fatal("owned idle scope")
	}
	props["TasksCurrent"] = "[not set]"
	if !ownFailedTransient(unit, props, map[string]bool{unit: true}) {
		t.Fatal("removed cgroup")
	}
	props["TasksCurrent"] = "1"
	if ownFailedTransient(unit, props, map[string]bool{unit: true}) {
		t.Fatal("live scope")
	}
}
