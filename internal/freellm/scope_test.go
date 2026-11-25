package freellm

import "testing"

func TestCardRepositorySelection(t *testing.T) {
	for _, tt := range []struct {
		card     Card
		eligible bool
	}{
		{Card{Project: "mcpshka", Paths: []string{"internal/projectci/projectci.go"}}, true},
		{Card{Project: "/repo/mcpshka", Paths: []string{"/repo/mcpshka/internal/x.go"}}, true},
		{Card{Project: "tabl"}, false},
		{Card{Project: "go2rtc", Paths: []string{".gitverse/workflows/ci.yml"}}, false},
		{Card{Paths: []string{"../lane/config"}}, false},
		{Card{Paths: []string{"/repo/mcpshka-other/file"}}, false},
		{Card{RequiresOtherProject: true}, false},
	} {
		got := Select(tt.card, "mcpshka", "/repo/mcpshka")
		if got.Eligible != tt.eligible || got.RequiresOtherProject == tt.eligible || (!got.Eligible && got.Reason == "") {
			t.Fatalf("%+v => %+v", tt.card, got)
		}
	}
	if got := Select(Card{}, "", ""); got.Eligible || got.Reason == "" {
		t.Fatal(got)
	}
}
