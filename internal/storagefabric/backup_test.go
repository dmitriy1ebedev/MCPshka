package storagefabric

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedStreamAuthenticatesTermination(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	plain := bytes.Repeat([]byte("private repo"), 20000)
	var sealed bytes.Buffer
	if e := Encrypt(&sealed, bytes.NewReader(plain), key); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(sealed.Bytes(), []byte("private repo")) {
		t.Fatal("plaintext leaked")
	}
	var out bytes.Buffer
	if e := Decrypt(&out, bytes.NewReader(sealed.Bytes()), key); e != nil || !bytes.Equal(out.Bytes(), plain) {
		t.Fatalf("roundtrip: %v", e)
	}
	for _, cut := range []int{1, 20, sealed.Len() - 20, sealed.Len() - 1} {
		if e := Decrypt(&bytes.Buffer{}, bytes.NewReader(sealed.Bytes()[:cut]), key); e == nil {
			t.Fatalf("truncation accepted at %d", cut)
		}
	}
	corrupt := append([]byte(nil), sealed.Bytes()...)
	corrupt[50] ^= 1
	if e := Decrypt(&bytes.Buffer{}, bytes.NewReader(corrupt), key); e == nil {
		t.Fatal("tamper accepted")
	}
	wrong := bytes.Repeat([]byte{1}, 32)
	if e := Decrypt(&bytes.Buffer{}, bytes.NewReader(sealed.Bytes()), wrong); e == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestRepositoryPlanRejectsEscape(t *testing.T) {
	e := Engine{Dir: t.TempDir()}
	for _, p := range []string{"/etc", "/root/repos/../secrets", "/root/repos", "/root/repos/a/b"} {
		if _, err := validateRepository(p); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	if err := atomicFile(filepath.Join(dir, "manifest"), []byte("data")); err != nil {
		t.Fatal(err)
	}
	_ = e
}
