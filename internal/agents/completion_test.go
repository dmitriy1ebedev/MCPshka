package agents

import (
	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
	"os/exec"
	"testing"
)

func TestCompletionRequiresBranchCommitsAndExactHeadTest(t *testing.T) {
	path := t.TempDir()
	gitRun := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return string(out)
	}
	gitRun("init", "-q", "-b", "task/with-a-long-branch-name")
	commit := func() { gitRun("-c", "user.name=t", "-c", "user.email=t@x", "commit", "-qm", "work", "--allow-empty") }
	commit()
	p := &policy.Policy{AllowedWorkdirs: []string{path}}
	g := gitops.New(p)
	before, _ := g.Revision(path)
	commit()
	after, _ := g.Revision(path)
	task := Task{Path: path, State: "done", Journal: &Journal{Branch: "task/with-a-long-branch-name", HeadBefore: before, HeadAfter: after}}
	good := func(root, head string) (bool, string) {
		if root != path || head != after {
			t.Fatal(root, head)
		}
		return true, "verified-job"
	}
	if got := VerifyCompletion(task, g, good); got.State != "done" {
		t.Fatal(got)
	}
	if got := VerifyCompletion(task, g, func(string, string) (bool, string) { return false, "old-head" }); got.State != "done_unverified" {
		t.Fatal(got)
	}
	task.Journal.HeadBefore = after
	if got := VerifyCompletion(task, g, good); got.State != "done_unverified" {
		t.Fatal(got)
	}
	task.Journal.HeadBefore = before
	task.Journal.Branch = "other"
	if got := VerifyCompletion(task, g, good); got.State != "done_unverified" {
		t.Fatal(got)
	}
	task.State = "quota"
	if got := VerifyCompletion(task, g, nil); got.State != "quota" {
		t.Fatal(got)
	}
}
