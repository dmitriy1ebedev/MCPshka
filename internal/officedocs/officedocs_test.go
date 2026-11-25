package officedocs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func spec(action string) Spec { return Spec{ID: "0123456789abcdef", Action: action} }

func TestPrepareBoundsSourceOutputAndBudget(t *testing.T) {
	src := filepath.Join(t.TempDir(), "опись.ods")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := spec("edit")
	if err := s.Prepare(src, ""); err != nil || s.Output != filepath.Join(WorkDir, s.ID, "опись.ods") || s.Path != src || s.MaxCells != 400 {
		t.Fatalf("%+v %v", s, err)
	}
	s = spec("edit")
	if err := s.Prepare(src, "отчёт.xlsx"); err != nil || !strings.HasSuffix(s.Output, "/отчёт.xlsx") {
		t.Fatalf("conversion target: %+v %v", s, err)
	}
	// A new document needs only a name; the result never leaves the run folder.
	s = spec("edit")
	if err := s.Prepare("", "новая.docx"); err != nil || filepath.Dir(s.Output) != RunDir(s.ID) || filepath.Dir(s.Path) != RunDir(s.ID) {
		t.Fatalf("new document: %+v %v", s, err)
	}
	for name, bad := range map[string]func(*Spec) error{
		"path traversal in save_as": func(s *Spec) error { return s.Prepare(src, "../../etc/cron.d/x.txt") },
		"unknown output type":       func(s *Spec) error { return s.Prepare(src, "x.exe") },
		"missing source":            func(s *Spec) error { return s.Prepare(filepath.Join(t.TempDir(), "no.ods"), "") },
		"new document on inspect":   func(s *Spec) error { s.Action = "inspect"; return s.Prepare("", "x.ods") },
		"ops on inspect": func(s *Spec) error {
			s.Action = "inspect"
			s.Ops = []map[string]any{{"op": "clear"}}
			return s.Prepare(src, "")
		},
		"op without a name": func(s *Spec) error { s.Ops = []map[string]any{{"range": "A1"}}; return s.Prepare(src, "") },
		"too many cells":    func(s *Spec) error { s.MaxCells = 20001; return s.Prepare(src, "") },
		"bad id":            func(s *Spec) error { s.ID = "../x"; return s.Prepare(src, "") },
	} {
		s := spec("edit")
		if bad(&s) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	link := filepath.Join(t.TempDir(), "link.ods")
	if os.Symlink("/etc/passwd", link) == nil {
		s = spec("inspect")
		if s.Prepare(link, "") == nil {
			t.Fatal("a symlink was accepted as a document")
		}
	}
}

// Running code (a script or a document macro) is the owner's act; changing
// cells or storing a macro's text is not.
func TestActsAsOwner(t *testing.T) {
	if (Spec{Ops: []map[string]any{{"op": "set_cells"}, {"op": "macro_set"}, {"op": "bind_event"}}}).ActsAsOwner() {
		t.Fatal("content ops must not need Face ID")
	}
	if !(Spec{Script: "x=1"}).ActsAsOwner() || !(Spec{Ops: []map[string]any{{"op": "macro_run"}}}).ActsAsOwner() {
		t.Fatal("code execution must need Face ID")
	}
}

func TestRunAcceptsOnlyToolSpecsAndResultOnlyRunOutputs(t *testing.T) {
	stray := filepath.Join(t.TempDir(), "spec.json")
	raw, _ := json.Marshal(spec("inspect"))
	os.WriteFile(stray, raw, 0o600)
	if err := Run(context.Background(), stray, os.Stdout); err == nil || !strings.Contains(err.Error(), "doc_inspect") {
		t.Fatalf("a spec outside the tool's folder ran: %v", err)
	}
	for _, p := range []string{"/etc/passwd", WorkDir + "/0123456789abcdef/../../x", WorkDir + "/specs/a.json", WorkDir + "/0123456789abcdef/.spec.json", WorkDir + "/0123456789abcdef/sub/x.ods"} {
		if _, _, err := Result(p); err == nil || !strings.Contains(err.Error(), "doc_edit run") {
			t.Fatalf("%s accepted as a run output: %v", p, err)
		}
	}
}

// The worker's plain-text path needs no LibreOffice; it is checked wherever
// python3 exists (CI images, H96).
func TestWorkerEditsPlainText(t *testing.T) {
	if _, err := os.Stat(python); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "office.py"), worker, 0o600)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("привет мир\nвторая\n"), 0o600)
	sp := Spec{ID: "0123456789abcdef", Action: "edit", Path: filepath.Join(dir, "a.txt"), Output: filepath.Join(dir, "b.md"), Workdir: dir,
		Ops: []map[string]any{{"op": "replace", "find": "мир", "replace": "MCPshka"}, {"op": "append", "text": "конец"}, {"op": "set_lines", "at": 2, "count": 1, "lines": []string{"2", "3"}}}}
	raw, _ := json.Marshal(sp)
	os.WriteFile(filepath.Join(dir, "spec.json"), raw, 0o600)
	out, err := runWorker(dir)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ := os.ReadFile(sp.Output)
	if string(got) != "привет MCPshka\n2\n3\nконец\n" || !strings.Contains(out, `"state": "done"`) || !strings.Contains(out, `"replaced": 1`) {
		t.Fatalf("%q\n%s", got, out)
	}
}
