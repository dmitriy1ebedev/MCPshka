package gitops

import "testing"

func TestLogResolvesRefWithoutShell(t *testing.T) {
	s, repo := makeRepo(t)
	out, err := s.Log(repo, "", 5)
	if err != nil || len(out.Commit) != 40 || len(out.Commits) == 0 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, bad := range []string{"--output=/tmp/x", "a..b", "HEAD;id", "-n1"} {
		if _, err := s.Log(repo, bad, 1); err == nil {
			t.Fatalf("ref %q accepted", bad)
		}
	}
}
