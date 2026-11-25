package webos

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testHostKey(t *testing.T, value byte) ssh.PublicKey {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.PublicKey(bytes.Repeat([]byte{value}, ed25519.PublicKeySize)))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testKnownHost(alias string, key ssh.PublicKey) string {
	return alias + " " + key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal()) + "\n"
}

func TestMCP196PairingUsesCheckedDescriptor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "known_hosts")
	content := testKnownHost(HostKeyAlias, testHostKey(t, 1))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	f, _, ok := openPairingFD(path)
	if !ok {
		t.Fatal("regular pairing file rejected")
	}
	defer f.Close()
	if err := os.Rename(path, filepath.Join(root, "previous")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "previous"), path); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err != nil || string(got) != content {
		t.Fatal("descriptor lost original file", err)
	}
	if _, _, ok := pinnedLGFingerprint(path); ok {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := openPairingFD(path); ok {
		t.Fatal("FIFO accepted")
	}
}

func TestMCP196PairingStatusSafeProjection(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "prisoner_key")
	pin := filepath.Join(root, "known_hosts")
	secret := "private-key-fixture"
	key := testHostKey(t, 2)
	known := testKnownHost("other-device", key) + testKnownHost(HostKeyAlias, key)
	if got := inspectPairing(t.Context(), root, ""); got.State != "HOLD" {
		t.Fatalf("missing files accepted: %+v", got)
	}
	if err := os.WriteFile(private, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pin, []byte(known), 0600); err != nil {
		t.Fatal(err)
	}
	got := inspectPairing(t.Context(), root, "")
	if got.State != "CONFIGURED_UNVERIFIED" || !got.KeyPresent || !got.KeyPermissionsOK ||
		!got.HostKeyPinned || got.HostKeyType != key.Type() || got.HostFingerprint != ssh.FingerprintSHA256(key) ||
		got.SSHAuthentication != "UNVERIFIED" || got.Reachability != "NOT_CHECKED" || got.ActualMutation {
		t.Fatalf("invalid pairing projection: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString(key.Marshal())) {
		t.Fatal("key material leaked", err)
	}
	if got := inspectPairing(t.Context(), root, "1.1.1.1"); got.Reachability != "UNSUPPORTED_ADDRESS" || got.State != "HOLD" {
		t.Fatalf("public address admitted: %+v", got)
	}
	if err := os.Chmod(private, 0644); err != nil {
		t.Fatal(err)
	}
	if got := inspectPairing(t.Context(), root, ""); got.KeyPresent || got.KeyPermissionsOK || got.State != "HOLD" {
		t.Fatalf("weak key mode accepted: %+v", got)
	}
	for _, contents := range []string{
		HostKeyAlias + " ssh-ed25519 YWJjZA==\n",
		known + testKnownHost(HostKeyAlias, testHostKey(t, 3)),
		known + "@revoked " + testKnownHost(HostKeyAlias, key),
		strings.Repeat("a", (64<<10)+1),
	} {
		if err := os.WriteFile(pin, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := pinnedLGFingerprint(pin); ok {
			t.Fatal("invalid or ambiguous host pin accepted")
		}
	}
}
