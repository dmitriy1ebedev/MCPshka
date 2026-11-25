package hosttriage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKernelLinkProtectionFlagsSwitchedOffKnobs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "fs"), 0o755); err != nil {
		t.Fatal(err)
	}
	set := func(name, v string) {
		if err := os.WriteFile(filepath.Join(root, "fs", name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"protected_hardlinks", "protected_symlinks", "protected_fifos", "protected_regular"} {
		set(n, "1")
	}
	if p := kernelLinkProtection(root); p != nil {
		t.Fatal("hardened host flagged", p)
	}
	set("protected_fifos", "2")
	set("protected_hardlinks", "0")
	p := kernelLinkProtection(root)
	if len(p) != 1 || p[0].Severity != "warning" || p[0].Evidence != "fs.protected_hardlinks=0" || !strings.Contains(p[0].Action.Args["run"].(string), "harden-links") {
		t.Fatal(p)
	}
}
