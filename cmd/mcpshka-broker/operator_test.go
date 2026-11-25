package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestProtectedRepositoryOperatorDeniesNonRoot(t *testing.T) {
	if os.Getenv("MCPSHKA_OPERATOR_TEST_CHILD") == "1" {
		os.Args = []string{"mcpshka-broker", "executor-prepare-repository", "0123456789abcdef0123456789abcdef"}
		if os.Getenv("MCPSHKA_OPERATOR_TEST_COMMAND") == "branch" {
			os.Args = []string{"mcpshka-broker", "executor-prepare-branch", "0123456789abcdef0123456789abcdef", "1123456789abcdef0123456789abcdef", "2123456789abcdef0123456789abcdef", "task-r1"}
		}
		main()
		os.Exit(0)
	}
	if os.Geteuid() == 0 {
		t.Skip("run in the native unprivileged test job")
	}
	for _, operation := range []string{"repository", "branch"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProtectedRepositoryOperatorDeniesNonRoot$")
		cmd.Env = append(os.Environ(), "MCPSHKA_OPERATOR_TEST_CHILD=1", "MCPSHKA_OPERATOR_TEST_COMMAND="+operation)
		out, err := cmd.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 || len(out) != 0 {
			t.Fatalf("%s operator did not deny before touching authority: exit=%v output=%q", operation, err, out)
		}
	}
}
