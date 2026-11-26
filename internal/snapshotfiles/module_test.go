package snapshotfiles

import (
	"errors"
	"strings"
	"testing"
)

func TestModulePathAllowlistsCommittedInputs(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum", "VERSION", "cmd/main.go", "internal/core/core_test.go",
		"packaging/deploy.sh", "README.md", "CHANGELOG.md", "BUGLOG.md",
		"docs/README.md", "docs/releases/RELEASE_0.11.9.md"} {
		if !ModulePath(name, ".gomodcache") {
			t.Errorf("excluded required path %q", name)
		}
	}
	for _, name := range []string{"", "/etc/shadow", ".git/config", "../escape", "cmd/../.env",
		"docs/nested/private.md", "docs/secret.key", ".env", ".gomodcache/foo",
		"cmd\\sub.go"} {
		if ModulePath(name, ".gomodcache") {
			t.Errorf("included forbidden path %q", name)
		}
	}
}

func TestModuleTreeFiltersDocsAndPreservesStrictFailure(t *testing.T) {
	input := strings.Join([]string{
		"100644 blob deadbeef\tREADME.md",
		"100644 blob deadbeef\tdocs/README.md",
		"100644 blob deadbeef\tdocs/releases/CHANGELOG.md",
		"100644 blob deadbeef\tdocs/nested/secret.md",
		"100644 blob deadbeef\tdocs/private.key",
		"100644 blob deadbeef\tcmd/main.go",
		"",
	}, "\x00")
	failed := errors.New("bad immutable tree")
	out, err := ModuleTree(func() ([]byte, error) { return []byte(input), nil }, failed)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, name := range []string{"README.md", "docs/README.md", "docs/releases/CHANGELOG.md", "cmd/main.go"} {
		if !strings.Contains(got, "\t"+name+"\x00") {
			t.Errorf("lost required input %q", name)
		}
	}
	for _, name := range []string{"docs/nested/secret.md", "docs/private.key"} {
		if strings.Contains(got, "\t"+name+"\x00") {
			t.Errorf("included unrelated input %q", name)
		}
	}
	if _, err := ModuleTree(func() ([]byte, error) { return []byte("broken\x00"), nil }, failed); !errors.Is(err, failed) {
		t.Fatalf("malformed Git entry must fail closed: %v", err)
	}
	if _, err := ModuleTree(func() ([]byte, error) { return nil, failed }, errors.New("other")); !errors.Is(err, failed) {
		t.Fatalf("Git read failure must be preserved: %v", err)
	}
}
