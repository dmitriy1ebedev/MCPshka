package knowledgeops

import (
	"errors"
	"fmt"
	"testing"

	"mcpshka/internal/failure"
	"mcpshka/internal/knowledge"
)

func TestKnowledgeInputRefusalKeepsRetryKey(t *testing.T) {
	for _, e := range []error{knowledge.ErrInvalid, fmt.Errorf("metadata.timestamp: in the future: %w", knowledge.ErrInvalid), knowledge.ErrLimit, errors.New("knowledge action requires add/update")} {
		got := knowledgeRefusal(e)
		if !failure.IsNoEffect(got) || !errors.Is(got, e) {
			t.Fatalf("%v: not a no-effect refusal: %v", e, got)
		}
	}
	if other := errors.New("disk I/O error"); failure.IsNoEffect(knowledgeRefusal(other)) {
		t.Fatal("an unknown store failure must stay UNKNOWN, not no-effect")
	}
}
