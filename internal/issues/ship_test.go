package issues

import (
	"path/filepath"
	"testing"
)

func TestFixedByAndShip(t *testing.T) {
	log := ParseLog("aaaaaaa1\x00x: something\n\nFixes MCP-160, MCP-161\nRefs MCP-159\x1e\nbbbbbbb2\x00old\n\nfixed: MCP-160\x1e")
	fixed := FixedBy(log)
	if len(fixed) != 2 || fixed["MCP-160"] != "aaaaaaa1" || fixed["MCP-161"] != "aaaaaaa1" {
		t.Fatalf("%v", fixed)
	}
	s := NewStore(filepath.Join(t.TempDir(), "i.json"))
	a, _ := s.Create(NewIssue{Title: "first problem", Priority: "P2", Body: "b"}, "t")
	b, _ := s.Create(NewIssue{Title: "second problem", Priority: "P2", Body: "b"}, "t")
	ok := func(Issue, string) error { return nil }
	r := s.Ship(map[string]string{a.ID: "aaaaaaa1", b.ID: "bbbbbbb2", "MCP-999": "ccccccc3"}, "0.10.0", ok)
	if len(r.Accepted) != 2 || len(r.Skipped) != 1 {
		t.Fatalf("%+v", r)
	}
	if got, _ := s.Get(a.ID); got.Status != Closed || got.Release != "0.10.0" || got.Commit != "aaaaaaa1" {
		t.Fatalf("%+v", got)
	}
	if r := s.Ship(map[string]string{a.ID: "aaaaaaa1"}, "0.10.0", ok); len(r.Accepted) != 0 {
		t.Fatalf("closed card accepted twice: %+v", r)
	}
}

func TestVersionLEAndShipped(t *testing.T) {
	for _, c := range [][3]string{{"0.9.9", "0.10.0", "y"}, {"0.10.0", "0.9.9", "n"}, {"0.7.3", "0.7.3", "y"}, {"x", "0.1.0", "n"}} {
		if VersionLE(c[0], c[1]) != (c[2] == "y") {
			t.Fatalf("%v", c)
		}
	}
	s := NewStore(filepath.Join(t.TempDir(), "i.json"))
	a, _ := s.Create(NewIssue{Title: "old fix", Priority: "P2", Body: "b"}, "t")
	b, _ := s.Create(NewIssue{Title: "future fix", Priority: "P2", Body: "b"}, "t")
	s.Update(a.ID, Patch{Status: Fixed, Commit: "aaaaaaa1", Release: "0.7.3"}, "t")
	s.Update(b.ID, Patch{Status: Fixed, Commit: "bbbbbbb2", Release: "0.11.0"}, "t")
	got := s.Shipped("0.10.0")
	if len(got) != 1 || got[a.ID] != "aaaaaaa1" {
		t.Fatalf("%v", got)
	}
	if r := s.Ship(got, "0.10.0", func(Issue, string) error { return nil }); len(r.Accepted) != 1 {
		t.Fatalf("%+v", r)
	}
	if i, _ := s.Get(a.ID); i.Release != "0.7.3" || i.Status != Closed {
		t.Fatalf("release must stay the shipping one: %+v", i)
	}
}
