package storagefabric

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealGitBundleEncryptedManifest(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) []byte {
		t.Helper()
		out, e := git(ctx, repo, args...)
		if e != nil {
			t.Fatalf("git %v: %v", args, e)
		}
		return out
	}
	run("init")
	os.WriteFile(filepath.Join(repo, "README"), []byte("version one"), 0600)
	run("add", "README")
	run("-c", "user.name=storage-test", "-c", "user.email=storage@test.invalid", "commit", "-m", "snapshot")
	head := strings.TrimSpace(string(run("rev-parse", "HEAD")))
	bundle := filepath.Join(t.TempDir(), "repo.bundle")
	run("bundle", "create", bundle, "--all")
	heads := string(run("bundle", "list-heads", bundle, "HEAD"))
	if !strings.HasPrefix(heads, head+" ") {
		t.Fatal("bundle HEAD is not pinned")
	}
	raw, e := os.ReadFile(bundle)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	m := Manifest{Version: 1, Bundles: []BundleManifest{{Name: "repo.bundle", Head: head, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw))}}}
	manifest, _ := json.Marshal(m)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(manifest)), Mode: 0600})
	tw.Write(manifest)
	tw.WriteHeader(&tar.Header{Name: "repo.bundle", Size: int64(len(raw)), Mode: 0600})
	tw.Write(raw)
	tw.Close()
	var encrypted, plain bytes.Buffer
	key := bytes.Repeat([]byte{8}, 32)
	if e = Encrypt(&encrypted, &archive, key); e != nil {
		t.Fatal(e)
	}
	if e = Decrypt(&plain, &encrypted, key); e != nil {
		t.Fatal(e)
	}
	verified, e := DecodeManifest(&plain)
	if e != nil || verified.Bundles[0].Head != head {
		t.Fatalf("manifest: %v", e)
	}
	run("bundle", "verify", bundle)
	clone := filepath.Join(t.TempDir(), "restored")
	if _, e = command(ctx, "/usr/bin/git", "clone", bundle, clone); e != nil {
		t.Fatal(e)
	}
	restored, e := git(ctx, clone, "rev-parse", "HEAD")
	if e != nil || strings.TrimSpace(string(restored)) != head {
		t.Fatalf("staging restore: %v", e)
	}
}
func TestStorageOperationOwnerAndRetryIdentity(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	e := Engine{Dir: dir}
	r := Request{Action: "mkdir", Provider: "drive", Path: "a", Owner: "owner", Key: "retry"}
	sum := sha256.Sum256([]byte(r.Owner + "\x00" + r.Key))
	r.ID = hex.EncodeToString(sum[:16])
	raw, _ := json.Marshal(r)
	fingerprint := sha256.Sum256(raw)
	op := operation{Request: r, Result: Result{Trust: "UNTRUSTED", ID: r.ID, State: "done"}, Fingerprint: hex.EncodeToString(fingerprint[:])}
	if err := e.save(op); err != nil {
		t.Fatal(err)
	}
	out, err := e.Do(context.Background(), r)
	if err != nil || out.ID != r.ID {
		t.Fatalf("idempotency: %v", err)
	}
	r.Path = "other"
	if _, err = e.Do(context.Background(), r); err == nil {
		t.Fatal("retry payload changed")
	}
	if _, err = e.load(op.Result.ID, "another-owner"); err == nil {
		t.Fatal("owner mismatch accepted")
	}
}

func TestGitObservationDoesNotRefreshIndex(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) []byte {
		t.Helper()
		out, e := git(ctx, repo, args...)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	run("init")
	file := filepath.Join(repo, "README")
	os.WriteFile(file, []byte("same contents"), 0600)
	run("add", "README")
	run("-c", "user.name=storage-test", "-c", "user.email=storage@test.invalid", "commit", "-m", "snapshot")
	before, _ := os.ReadFile(filepath.Join(repo, ".git", "index"))
	older := time.Now().Add(-2 * time.Hour)
	os.Chtimes(file, older, older)
	run("status", "--porcelain", "--untracked-files=no")
	after, _ := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if !bytes.Equal(before, after) {
		t.Fatal("read-only backup observation rewrote repository index")
	}
}
