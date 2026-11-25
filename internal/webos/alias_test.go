package webos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinHostAliasCopiesSingleKeyOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(p, []byte("|1|salt|hash ssh-rsa AAAAkey\n"), 0600)
	pinHostAlias(p)
	pinHostAlias(p)
	raw, _ := os.ReadFile(p)
	if strings.Count(string(raw), HostKeyAlias+" ssh-rsa AAAAkey\n") != 1 {
		t.Fatalf("alias not pinned once: %q", raw)
	}
	two := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(two, []byte("a ssh-rsa K1\nb ssh-rsa K2\n"), 0600)
	pinHostAlias(two)
	if raw, _ := os.ReadFile(two); strings.Contains(string(raw), HostKeyAlias) {
		t.Fatal("ambiguous keys must not be pinned")
	}
}
