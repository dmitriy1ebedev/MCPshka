package executor

import (
	"strings"
	"testing"
)

func TestPipelineWorkerKeepsProfileArgvPositional(t *testing.T) {
	spec := RunSpec{Profile: "recipe-test-v1", Command: []string{"make", "verify", "VALUE=$(touch /host)"}, TimeoutSeconds: 60, WorkspaceDevice: 1, WorkspaceInode: 1, StageSpec: StageSpec{Stage: "verify"}}
	if err := spec.validate(); err != nil {
		t.Fatal(err)
	}
	args := workerArgs("", spec)
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "--ro-bind-fd\n3\n/workspace") || !strings.Contains(joined, "cp -R /workspace /scratch/project") {
		t.Fatal("source isolation or scratch copy missing")
	}
	if args[len(args)-1] != spec.Command[2] || args[len(args)-3] != "make" {
		t.Fatal("profile argv interpreted as shell syntax")
	}
	spec.Stage = "deploy"
	if spec.validate() == nil {
		t.Fatal("unknown stage accepted")
	}
	spec.Stage = "verify"
	spec.Artifacts = []string{"out"}
	if spec.validate() == nil {
		t.Fatal("non-package exports accepted")
	}
}
