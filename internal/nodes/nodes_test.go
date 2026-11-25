package nodes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNode points Dir at a temp key directory and ssh-keyscan at a script
// that prints the host key in hostKey (swappable to simulate a changed key).
func fakeNode(t *testing.T) (hostKey string) {
	t.Helper()
	tmp := t.TempDir()
	// ssh-keygen refuses to run for a uid without a passwd entry (sealed
	// test sandbox); the service user always has one.
	if out, err := exec.Command(sshKeygen, "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(tmp, "probe")).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unusable here: %v %s", err, out)
	}
	Dir, OOBConfig = filepath.Join(tmp, "ssh"), filepath.Join(tmp, "oob.json")
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	hostKey = filepath.Join(tmp, "host.line")
	script := filepath.Join(tmp, "keyscan")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat "+hostKey+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := sshKeyscan
	sshKeyscan = script
	t.Cleanup(func() { sshKeyscan = old })
	return hostKey
}

func presentKey(t *testing.T, hostKey string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command(sshKeygen, "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, "k")).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "k.pub"))
	f := strings.Fields(string(pub))
	if err := os.WriteFile(hostKey, []byte("203.0.113.7 "+f[0]+" "+f[1]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fps, err := fingerprints(context.Background(), []byte("x "+f[0]+" "+f[1]+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	return fps[0]
}

// MCP-147: add a node, see NEW, accept the shown fingerprint, MATCH; the
// node changes its key — CHANGED, the old fingerprint is refused, the new
// one accepted; remove moves the files aside.
func TestNodeLifecycleAndChangedKey(t *testing.T) {
	ctx := context.Background()
	hostKey := fakeNode(t)
	n, err := Add(ctx, "stand-1", Addr{Host: "203.0.113.7", Port: 22, User: "mcpshka-build"})
	if err != nil || !strings.HasPrefix(n.PublicKey, "restrict ssh-ed25519 ") || !strings.HasSuffix(n.PublicKey, "mcpshka-stand-1") {
		t.Fatal("add", n, err)
	}
	first := presentKey(t, hostKey)
	if hk, _, err := Scan(ctx, "stand-1"); err != nil || hk.State != "NEW" || hk.Fingerprints[0] != first {
		t.Fatal("scan new", hk, err)
	}
	if hk, err := Accept(ctx, "stand-1", first); err != nil || hk.State != "MATCH" {
		t.Fatal("accept", hk, err)
	}
	if hk, _, _ := Scan(ctx, "stand-1"); hk.State != "MATCH" {
		t.Fatal("pinned key not matched", hk)
	}
	second := presentKey(t, hostKey)
	if hk, _, _ := Scan(ctx, "stand-1"); hk.State != "CHANGED" || hk.Pinned[0] != first {
		t.Fatal("changed key not reported", hk)
	}
	if _, err := Accept(ctx, "stand-1", first); err == nil {
		t.Fatal("stale fingerprint accepted for a changed key")
	}
	if hk, err := Accept(ctx, "stand-1", second); err != nil || hk.State != "MATCH" {
		t.Fatal("new key not accepted", hk, err)
	}
	if list, _ := List(); len(list) != 1 || list[0].Addr == nil || list[0].Addr.User != "mcpshka-build" || len(list[0].Pinned) != 1 {
		t.Fatal("list", list)
	}
	if err := Remove("stand-1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := List(); len(list) != 0 {
		t.Fatal("removed node still listed", list)
	}
}

func TestLegacyAddressAndValidation(t *testing.T) {
	fakeNode(t)
	for _, f := range []string{"compute-1", "compute-1.pub"} {
		if err := os.WriteFile(filepath.Join(Dir, f), []byte("ssh-ed25519 AAAA x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{"paths":[{"name":"vps","target":"build@198.51.100.2","key":"` + filepath.Join(Dir, "compute-1") + `"}]}`
	if err := os.WriteFile(OOBConfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := Get("compute-1"); n.Addr == nil || n.Addr.Host != "198.51.100.2" || n.Addr.User != "build" {
		t.Fatal("legacy address", n)
	}
	for _, bad := range []struct {
		name string
		a    Addr
	}{
		{"Bad Name", Addr{Host: "198.51.100.2", Port: 22, User: "u"}},
		{"ok", Addr{Host: "evil;rm", Port: 22, User: "u"}},
		{"ok", Addr{Host: "198.51.100.2", Port: 0, User: "u"}},
		{"ok", Addr{Host: "198.51.100.2", Port: 22, User: "-oProxyCommand"}},
	} {
		if _, err := Add(context.Background(), bad.name, bad.a); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
