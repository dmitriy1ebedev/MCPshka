package fstools

import (
	"mcpshka/internal/filesystem"
	"testing"
	"time"
)

func TestSearchSessionIsolationLimitExpiry(t *testing.T) {
	s := &searchSessions{items: map[string]searchSession{}}
	id, err := s.start("a", filesystem.FindQuery{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.get(id, "b", false); err == nil {
		t.Fatal("foreign session")
	}
	if q, err := s.get(id, "a", false); err != nil || q.Pattern != "needle" {
		t.Fatal(q, err)
	}
	for len(s.items) < 32 {
		if _, err = s.start("a", filesystem.FindQuery{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.start("a", filesystem.FindQuery{}); err == nil {
		t.Fatal("unbounded sessions")
	}
	s.items[id] = searchSession{owner: "a", last: time.Now().Add(-11 * time.Minute)}
	if _, err = s.get(id, "a", false); err == nil {
		t.Fatal("expired session")
	}
	next, err := s.start("a", filesystem.FindQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.get(next, "a", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.get(next, "a", false); err == nil {
		t.Fatal("stopped session")
	}
}
