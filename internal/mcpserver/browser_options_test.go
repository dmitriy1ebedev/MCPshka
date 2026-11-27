package mcpserver

import (
	"testing"
	"time"
)

func TestBrowserWorkerReceivesOperatorPolicyAndProcessBudget(t *testing.T) {
	t.Setenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST", "192.168.77.10:8123")
	opts := browserJobOptions(30 * time.Second)
	if opts.Timeout != 40*time.Second || opts.Env["MCPSHKA_BROWSER_HTTP_ALLOWLIST"] != "192.168.77.10:8123" {
		t.Fatalf("worker lost policy or process budget: %+v", opts)
	}
}

func TestBrowserStorageImportRequiresOwnerSession(t *testing.T) {
	if !browserActs(BrowserRunInput{LocalStorage: map[string]string{"hassTokens": "owner-export"}}) {
		t.Fatal("session import bypasses Face ID classification")
	}
}
