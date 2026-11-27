package projectlease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
)

func TestResumeFencesOldTokenReplaysAndPreservesDirtyWork(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(0)
	d := deploy.New(s.policy, filepath.Join(root, "deploy"))
	guard := func(string, string) error { return nil }
	old, err := s.AcquireTracked(root, "chat-family:first", strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "work.txt")
	if err := os.WriteFile(name, []byte("unfinished work"), 0600); err != nil {
		t.Fatal(err)
	}
	in := ResumeInput{Path: root, Token: old.Token, Owner: "chat-family:second", IdempotencyKey: "handoff"}
	wrong := in
	wrong.Token = "wrong"
	if _, err := s.Resume(wrong, m, d, guard); err == nil {
		t.Fatal("family name granted ownership")
	}
	next, err := s.Resume(in, m, d, guard)
	if err != nil {
		t.Fatal(err)
	}
	if next.Token == old.Token || next.Status.Generation != old.Status.Generation+1 || next.Status.LeaseID != old.Status.LeaseID || next.Status.ExecutionID == old.Status.ExecutionID {
		t.Fatal("handoff did not fence the previous incarnation")
	}
	if _, err := s.HeartbeatRefresh(root, old.Token); err == nil {
		t.Fatal("previous writer renewed")
	}
	if _, err := s.Release(root, old.Token); err == nil {
		t.Fatal("previous writer released successor")
	}
	if _, err := s.AcquireTracked(root, "third", strings.Repeat("d", 32)); err == nil {
		t.Fatal("two writers admitted")
	}
	replay, err := s.Resume(in, m, d, guard)
	if err != nil || replay.Token != next.Token {
		t.Fatal("lost response not replayable", err)
	}
	work, err := os.ReadFile(name)
	if err != nil || string(work) != "unfinished work" {
		t.Fatal("dirty work lost")
	}
	if _, err := s.Release(root, next.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(in, m, d, guard); err == nil {
		t.Fatal("completed handoff resurrected")
	}
}

func TestConcurrentResumeCannotCreateTwoWriters(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(0)
	d := deploy.New(s.policy, filepath.Join(root, "deploy"))
	old, err := s.AcquireTracked(root, "first", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		holder AcquireResult
		err    error
	}
	results := make(chan result, 2)
	for _, owner := range []string{"successor-a", "successor-b"} {
		go func(owner string) {
			service := New(s.dir, s.policy)
			holder, err := service.Resume(ResumeInput{Path: root, Token: old.Token, Owner: owner, IdempotencyKey: owner}, m, d, func(string, string) error { return nil })
			results <- result{holder, err}
		}(owner)
	}
	winners := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil {
			winners++
			if _, err := s.ValidateHolder(root, r.holder.Token); err != nil {
				t.Fatal(err)
			}
		}
	}
	if winners != 1 {
		t.Fatal("expected exactly one successor", winners)
	}
}
