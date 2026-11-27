package project

import (
	"slices"
	"testing"
)

// MCP-270: the profile's registered argv must reach the sealed matcher as is.
func TestGoTestNoTimeoutKeepsProfileArgv(t *testing.T) {
	profile := TestPlan{Kind: "profile", Command: "go", Args: []string{"test", "./pkg/..."}}
	if got := GoTestNoTimeout(profile); !slices.Equal(got, profile.Args) {
		t.Fatalf("profile argv changed: %v", got)
	}
	targeted := TestPlan{Kind: "go", Command: "go", Args: []string{"test", "./pkg"}}
	if got := GoTestNoTimeout(targeted); !slices.Equal(got, []string{"test", "-timeout=0", "./pkg"}) {
		t.Fatalf("targeted go test keeps its no-timeout flag: %v", got)
	}
}
