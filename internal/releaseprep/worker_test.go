package releaseprep

import (
	"mcpshka/internal/policy"
	"testing"
)

func TestPreparationDoesNotRequireAnInterpreterException(t *testing.T) {
	if got := policy.Default().ClassifyCommand(Command, []string{"release-artifact", "{}"}); got == policy.ClassPrivileged {
		t.Fatal("native release preparation still requires a forbidden interpreter")
	}
}
