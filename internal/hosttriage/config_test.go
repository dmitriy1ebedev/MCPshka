package hosttriage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDoctorNamesBrokenFilesWithoutValues(t *testing.T) {
	dir := t.TempDir()
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("llm.json", "{\n  \"token\": \"sekret\",\n  oops\n}")
	put("llm.json.bak-20261124", "{}")
	put("auth.json", `{"ok": true}`)
	put("tunnel.env", "# c\nA=1\nexport B=2\n")
	put("mcpshka.env", "A=1\nsekret line\n")
	put("broken.json.bak-1", "{")
	p := configProblems(dir)
	if len(p) != 2 {
		t.Fatalf("want llm.json and mcpshka.env, got %+v", p)
	}
	for _, x := range p {
		if strings.Contains(x.Evidence+x.Note, "sekret") {
			t.Fatalf("a value leaked: %+v", x)
		}
	}
	byName := map[string]TriageProblem{}
	for _, x := range p {
		byName[filepath.Base(x.Subject)] = x
	}
	if e := byName["llm.json"]; e.Evidence != "invalid JSON at line 3" || !strings.Contains(e.Note, "llm.json.bak-20261124") {
		t.Fatalf("llm.json: %+v", e)
	}
	if e := byName["mcpshka.env"]; e.Evidence != "line 2 is not KEY=VALUE" {
		t.Fatalf("mcpshka.env: %+v", e)
	}
}
