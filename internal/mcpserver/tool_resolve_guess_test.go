package mcpserver

import "testing"

// 26.11 audit: chats guessed tool names and got CAPABILITY_MISSING although
// the tool exists under another name.
func TestGuessedToolNamesResolveToRealTools(t *testing.T) {
	c := catalogClient(t, nil)
	for goal, want := range map[string]string{"git_push": "git_provider_push", "issue_search": "issue_list"} {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": goal, "compact": true})
		cands, _ := r["candidates"].([]any)
		found := false
		for _, x := range cands {
			found = found || x.(map[string]any)["name"] == want
		}
		if !found {
			t.Fatalf("%s: want %s among %v", goal, want, r)
		}
	}
}
