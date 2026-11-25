package executor

import "testing"

func TestProtectedFixtureSkipsRootOrigin(t *testing.T) {
	t.Setenv("MCPSHKA_EXECUTOR_TEST_AS_ROOT", "service-user")
	reached := false
	t.Run("root_origin", func(t *testing.T) {
		protectedBirthFixture(t)
		reached = true
	})
	if reached {
		t.Fatal("root-origin protected/candidate fixtures must skip with a service-user requirement")
	}
}
