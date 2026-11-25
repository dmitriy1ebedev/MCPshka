package dockerops

import "testing"

func TestNewFromEnvironmentBrokerOptIn(t *testing.T) {
	t.Setenv("MCPSHKA_PRIVILEGED_BROKER", "")
	if svc := NewFromEnvironment(); svc.brokered {
		t.Fatal("broker unexpectedly enabled by default")
	}
	t.Setenv("MCPSHKA_PRIVILEGED_BROKER", "true")
	if svc := NewFromEnvironment(); !svc.brokered {
		t.Fatal("broker opt-in did not select broker runner")
	}
}
