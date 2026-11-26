package systemdsvc

import (
	"context"
	"strings"
	"testing"
)

func TestWorkflowExcerptIsBoundedAndPrivate(t *testing.T) {
	raw := "executor HOLD: admission or fencing unverified\nlease_token=abc123 password: shh Authorization: Bearer credentials https://u:p@example.org\n"
	got := DiagnosticText(raw)
	for _, secret := range []string{"abc123", "shh", "credentials", "u:p"} {
		if strings.Contains(got, secret) {
			t.Fatal(got)
		}
	}
	if !strings.Contains(got, "admission or fencing unverified") {
		t.Fatal(got)
	}
	if len(DiagnosticText(strings.Repeat("x", 9000))) > 4096 {
		t.Fatal("unbounded")
	}
	for _, unit := range []string{"ssh", "mcpshka-roadmap-*", "../mcpshka-roadmap-x", "mcpshka-integration-admin-bad"} {
		if _, err := WorkflowExcerpt(context.Background(), unit); err == nil {
			t.Fatal("unsafe unit", unit)
		}
	}
}
