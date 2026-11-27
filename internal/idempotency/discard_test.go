package idempotency

import (
	"errors"
	"mcpshka/internal/failure"
	"testing"
)

func TestDiscardReleasesOnlyMatchingPendingRecord(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Begin("k", "op", "fp1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Discard("k", "op", "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign fingerprint discarded: %v", err)
	}
	if err := s.Discard("k", "op", "fp1"); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Begin("k", "op", "fp2"); err != nil || !b.Fresh {
		t.Fatalf("released key not reusable: %+v %v", b, err)
	}
	if err := s.Complete("k", "op", "fp2", map[string]string{"ok": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Discard("k", "op", "fp2"); err == nil {
		t.Fatal("terminal record discarded")
	}
}

func TestRunFreesKeyAfterNoEffectRefusal(t *testing.T) {
	s := New(t.TempDir())
	refuse := func() (int, error) { return 0, failure.Rejected("BAD", "test", "bad input", nil) }
	if _, err := Run(s, "k", "op", "same", refuse); !failure.IsNoEffect(err) {
		t.Fatal(err)
	}
	if got, err := Run(s, "k", "op", "same", func() (int, error) { return 7, nil }); err != nil || got != 7 {
		t.Fatalf("retry with the same key: %d %v", got, err)
	}
	if _, err := Run(s, "k2", "op", "same", func() (int, error) { return 0, errors.New("boom") }); err == nil {
		t.Fatal("want error")
	}
	if _, err := Run(s, "k2", "op", "same", func() (int, error) { return 1, nil }); err == nil {
		t.Fatal("ordinary failure must keep the key")
	}
}
