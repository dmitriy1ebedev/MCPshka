package opencore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAuditFindsEachKindAndMasksSecrets(t *testing.T) {
	root := tree(t, map[string]string{
		"a.go":             "// owner: someone@mail.ru, noreply@anthropic.com, demo@example.com\nhost := \"h96.tail1234.ts.net\"\nip := \"192.168.31.250\" // and 100.100.1.2 and 10.0.0.1\n",
		"docs/run.md":      "cd /root/repos/mcpshka && ls /home/dima/x\n",
		"cfg.yml":          "token: \"ghp_ABCDEFGHIJKLMNOPQRSTUVWX\"\npassword = \"hunter2hunter2\"\n-----BEGIN OPENSSH PRIVATE KEY-----\n",
		"version.txt":      "go 1.27.0 and 0.19.0 are versions, 8.8.8.8 is public\n",
		".git/config":      "url = someone@mail.ru\n",
		"testdata/fx.json": "someone@mail.ru",
		"bin.dat":          "x\x00someone@mail.ru",
		"blob":             "someone@mail.ru\x00",
	})
	r, err := Audit(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"email": 1, "tailnet": 1, "private_ip": 3, "home_path": 1, "secret": 4}
	for k, n := range want {
		if r.ByKind[k] != n {
			t.Errorf("%s: %d, want %d (%+v)", k, r.ByKind[k], n, r.Findings)
		}
	}
	for _, f := range r.Findings {
		if f.Kind == "secret" && (strings.Contains(f.Excerpt, "IJKLMNOP") || strings.Contains(f.Excerpt, "hunter2hunter2")) {
			t.Fatalf("secret not masked: %+v", f)
		}
		if strings.HasPrefix(f.File, ".git") || strings.HasPrefix(f.File, "testdata") || f.File == "blob" {
			t.Fatalf("skipped path audited: %+v", f)
		}
		if f.Line == 0 || f.File == "" {
			t.Fatalf("location missing: %+v", f)
		}
	}
}

func TestAuditLimitTruncatesButCountsAll(t *testing.T) {
	root := tree(t, map[string]string{"a.md": strings.Repeat("x@mail.ru\n", 10)})
	r, err := Audit(root, 3)
	if err != nil || len(r.Findings) != 3 || !r.Truncated || r.ByKind["email"] != 10 {
		t.Fatalf("%+v %v", r, err)
	}
}
