package agents

import (
	"strings"
	"testing"
)

func TestQuotaFinalIgnoresEarlierMentions(t *testing.T) {
	done := "read: when one hits its usage limit\n" + strings.Repeat("work\n", 200) + "Реализовано, дерево чистое."
	if quotaFinal(done) {
		t.Fatal("finished task with an early limit mention was classified as quota")
	}
	if !quotaFinal(strings.Repeat("work\n", 200) + "You've hit your usage limit. Try again at 1:42 AM.") {
		t.Fatal("final usage limit message not classified as quota")
	}
}
