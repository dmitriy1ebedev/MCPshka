package issues

import (
	"path/filepath"
	"testing"
)

func TestDiscoveryCasesAreBoundedAndInvalidReportsDoNotCount(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	i, err := s.Create(NewIssue{Title: "Discovery", Body: "fixture", Priority: "P2"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	valid := &DiscoveryDefect{RegressionCase: "Read files", NativeAlternatives: []string{"fs_read"}}
	for n := 0; n < 20; n++ {
		if _, err := s.Update(i.ID, Patch{Discovery: valid}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*DiscoveryDefect{{}, {RegressionCase: "bad tool", NativeAlternatives: []string{"fs_read\nexec_start"}}} {
		if _, err := s.Update(i.ID, Patch{Discovery: d}, "test"); err == nil {
			t.Fatal("invalid report accepted")
		}
	}
	got, err := s.Get(i.ID)
	if err != nil || got.DiscoveryComplaints != 20 || len(got.DiscoveryCases) != 16 {
		t.Fatalf("%+v %v", got, err)
	}
}
