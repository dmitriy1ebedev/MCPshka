package mcpserver

import (
	"strings"
	"testing"
)

func TestExecStartRoutesTestsToProjectTest(t *testing.T) {
	for _, c := range [][]string{{"go", "test", "./..."}, {"/usr/local/go/bin/go", "test", "-run", "X"}, {"npm", "test"}, {"python3", "-m", "pytest"}, {"pytest", "-q"}} {
		err := testRunnerRoute(c[0], c[1:], "/root/repos/x")
		if err == nil || !strings.Contains(err.Error(), "TEST_ROUTE_PROJECT_TEST") {
			t.Fatal("test runner not routed", c, err)
		}
	}
	for _, c := range [][]string{{"go", "build", "./..."}, {"go", "vet"}, {"git", "status"}, {"npm", "ci"}, {"python3", "-m", "pip"}} {
		if err := testRunnerRoute(c[0], c[1:], "/root/repos/x"); err != nil {
			t.Fatal("non-test refused", c, err)
		}
	}
}
