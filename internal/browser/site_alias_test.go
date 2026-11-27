package browser

import "testing"

func TestGigaIDEAliasUsesOnlyOperatorWorkspace(t *testing.T) {
	for _, raw := range []string{"", "http://ide.example/x", "https://user@ide.example/x", "https://ide.example/x#fragment"} {
		t.Setenv("MCPSHKA_GIGAIDE_WORKSPACE_URL", raw)
		if OperatorSite("gigaide") != "gigaide" {
			t.Fatal("unsafe alias", raw)
		}
	}
	t.Setenv("MCPSHKA_GIGAIDE_WORKSPACE_URL", "https://ide.example/workspace")
	if OperatorSite("gigaide") != "ide.example" || OperatorSite("gitverse.ru") != "gitverse.ru" {
		t.Fatal("wrong saved profile")
	}
}
