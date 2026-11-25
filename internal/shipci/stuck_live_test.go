package shipci

import (
	"context"
	"testing"
)

// ci_status lists only the newest run; the streak comes from recent_outcomes.
func TestGitVerseStuckSeesLiveStatusShape(t *testing.T) {
	var calls []string
	c := map[string]any{"status": "cancelled", "conclusion": nil}
	g := fake(map[string]map[string]any{
		"gitverse/status": {"state": "HOLD", "diagnosis": map[string]any{"code": "CI_PLATFORM_CANCELLED"}, "runs": []any{c}, "recent_outcomes": []any{"cancelled", "cancelled", "cancelled"}},
	}, &calls)
	if stage, _, _, _ := g.GitVerse(context.Background()); stage != "CI_STUCK" {
		t.Fatalf("live shape: stage %q", stage)
	}
	g = fake(map[string]map[string]any{
		"gitverse/status": {"state": "HOLD", "diagnosis": map[string]any{"code": "CI_PLATFORM_CANCELLED"}, "runs": []any{c}, "recent_outcomes": []any{"cancelled", "success", "cancelled"}},
	}, &calls)
	if stage, _, _, _ := g.GitVerse(context.Background()); stage == "CI_STUCK" {
		t.Fatal("one cancellation after a pass is not stuck")
	}
}
