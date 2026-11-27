package memory

import (
	"strings"
	"testing"
)

func TestExpandAddsRecordSpelling(t *testing.T) {
	got := expand("гит от мспишки, лиз висит, фейс айди")
	for _, want := range []string{"git", "mcpshka", "lease", "face id"} {
		if !strings.Contains(got, want) {
			t.Errorf("expand lost %q: %s", want, got)
		}
	}
	if got := expand("версии сессии"); got != "версии сессии" {
		t.Errorf("a word ending is not an owner word: %s", got)
	}
	if w, _ := matchQueries("что надо делать"); w == "" {
		t.Error("a query of stop words only must still search")
	}
	if w, _ := matchQueries("что делать с лизом"); strings.Contains(w, `"что"`) {
		t.Errorf("stop word left in the query: %s", w)
	}
}
