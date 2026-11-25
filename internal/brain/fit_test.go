package brain

import (
	"strings"
	"testing"

	"mcpshka/internal/llmgateway"
)

func TestFitShortensOldestResultsOnly(t *testing.T) {
	big := strings.Repeat("x", 10000)
	msgs := []llmgateway.Message{{Role: "user", Content: "задача"}}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, llmgateway.Message{Role: "assistant", Content: "шаг"}, llmgateway.Message{Role: "function", Content: big})
	}
	out := fit(msgs, 40000)
	size := 0
	for _, m := range out {
		size += len(m.Content)
	}
	if size > 40000 || out[0].Content != "задача" || out[len(out)-1].Content != big || len(out[2].Content) >= 1000 {
		t.Fatalf("size %d, first result %d bytes", size, len(out[2].Content))
	}
}
