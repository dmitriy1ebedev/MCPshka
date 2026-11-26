package repopath

import "testing"

// The live worktree of this checkout names its repository; anything else is "".
func TestWorktree(t *testing.T) {
	if got := Worktree("/root/repos/wt/mcpshka-feat-0.12.6"); got != "" && got != "/root/repos/mcpshka" {
		t.Fatalf("worktree: %q", got)
	}
	for _, p := range []string{"/root/repos/mcpshka", "/root/repos/wt/../mcpshka", "/tmp/x", "/root/repos/wt/a/b"} {
		if got := Worktree(p); got != "" {
			t.Fatalf("%s: %q", p, got)
		}
	}
}

func TestShape(t *testing.T) {
	if !Shape("/root/repos/wt/mcpshka-codex-0.12.9", "/root/repos/mcpshka") {
		t.Fatal("valid shape refused")
	}
	for _, c := range [][2]string{{"/root/repos/mcpshka", "/root/repos/mcpshka"}, {"/root/repos/wt/a/b", "/root/repos/a"}, {"/root/repos/wt/x", "/root/repos/wt/y"}, {"/root/repos/wt/x", "/etc"}} {
		if Shape(c[0], c[1]) {
			t.Fatalf("%v accepted", c)
		}
	}
}
