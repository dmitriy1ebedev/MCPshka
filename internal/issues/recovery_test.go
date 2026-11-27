package issues

import (
	"path/filepath"
	"testing"
)

// An accepted look-alike hands its fix and lesson to the next card.
func TestRecoveriesFromAcceptedLookAlikes(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	old, err := s.Create(NewIssue{Title: "Zigbee2MQTT падает под тяжёлым go test", Priority: "P1", Body: "x"}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(old.ID, Patch{Status: Fixed, Commit: "abc1234"}, "ai"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(old.ID, "job:ok", "веса дома", "ai", func(Issue, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLesson(old.ID, "дом впереди тяжёлого"); err != nil {
		t.Fatal(err)
	}
	open, _ := s.Create(NewIssue{Title: "Zigbee2MQTT падает под тяжёлым сборкой", Priority: "P1", Body: "y"}, "ai")
	r := s.Recoveries(open)
	if len(r) != 1 || r[0].ID != old.ID || r[0].Commit != "abc1234" || r[0].Lesson == "" || r[0].Note != "веса дома" {
		t.Fatalf("%+v", r)
	}
}
