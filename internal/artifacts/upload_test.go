package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func uploadFixture(t *testing.T) (*Inbox, string) {
	t.Helper()
	previous := inboxStatfs
	inboxStatfs = func(_ int, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 10 << 18}
		return nil
	}
	t.Cleanup(func() { inboxStatfs = previous })
	dir := t.TempDir()
	db, e := statestore.Open(filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	return NewInbox(filepath.Join(dir, "inbox"), db), dir
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func beginUpload(t *testing.T, i *Inbox, key string, b []byte) Upload {
	t.Helper()
	u, e := i.Begin("chat", key, "ROADMAP.md", int64(len(b)), digest(b))
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func TestUploadResumeIntegrityAndOwner(t *testing.T) {
	i, _ := uploadFixture(t)
	b := []byte("prepared roadmap")
	u := beginUpload(t, i, "upload", b)
	if _, e := i.Status("other-chat", u.ID); e == nil {
		t.Fatal("cross-principal disclosure")
	}
	if _, e := i.Chunk("chat", u.ID, 1, b[:3]); e == nil {
		t.Fatal("wrong offset accepted")
	}
	if _, e := i.Chunk("chat", u.ID, 0, b[:3]); e != nil {
		t.Fatal(e)
	}
	reopened := NewInbox(i.dir, i.db)
	if s, e := reopened.Status("chat", u.ID); e != nil || s.Offset != 3 {
		t.Fatalf("restart status: %+v %v", s, e)
	}
	if s, e := reopened.Chunk("chat", u.ID, 0, b[:3]); e != nil || s.Offset != 3 {
		t.Fatalf("duplicate append: %+v %v", s, e)
	}
	if _, e := reopened.Chunk("chat", u.ID, 0, []byte("bad")); e == nil {
		t.Fatal("conflicting replay accepted")
	}
	if _, e := reopened.Finish("chat", u.ID); e == nil {
		t.Fatal("incomplete seal")
	}
	if _, e := reopened.Chunk("chat", u.ID, 3, b[3:]); e != nil {
		t.Fatal(e)
	}
	s, e := reopened.Finish("chat", u.ID)
	if e != nil || s.State != "ready" || s.Trust != "UNTRUSTED" {
		t.Fatalf("seal: %+v %v", s, e)
	}
	got, e := os.ReadFile(s.Path)
	if e != nil || string(got) != string(b) {
		t.Fatal("content changed", e)
	}
	if _, e := reopened.Cancel("chat", u.ID); e == nil {
		t.Fatal("completed artifact deleted")
	}
	if _, e := reopened.Finish("chat", u.ID); e != nil {
		t.Fatal("seal replay", e)
	}
	if st, e := os.Stat(s.Path); e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("unsafe blob mode")
	}
}
func TestUploadValidationAndChecksums(t *testing.T) {
	i, _ := uploadFixture(t)
	for _, name := range []string{"../ROADMAP.md", "/root/file", "a\\b", "a\n.md", "."} {
		if _, e := i.Begin("chat", name, name, 1, digest([]byte("a"))); e == nil {
			t.Fatalf("unsafe name %q", name)
		}
	}
	if _, e := i.Begin("chat", "large", "file", MaxUploadSize+1, strings.Repeat("a", 64)); e == nil {
		t.Fatal("unbounded upload")
	}
	if _, e := i.Begin("chat", "hash", "file", 1, "SHA"); e == nil {
		t.Fatal("invalid digest")
	}
	u := beginUpload(t, i, "wrong", []byte("abc"))
	if _, e := i.Chunk("chat", u.ID, 0, []byte("xyz")); e != nil {
		t.Fatal(e)
	}
	if _, e := i.Finish("chat", u.ID); e == nil {
		t.Fatal("incorrect full digest")
	}
	if _, e := i.Cancel("chat", u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(i.dir, u.ID+".blob")); !os.IsNotExist(e) {
		t.Fatal("cancel did not remove owned partial")
	}
	if _, e := i.Begin("chat", "wrong", "changed", 3, digest([]byte("abc"))); e == nil {
		t.Fatal("retry mismatch")
	}
	u = beginUpload(t, i, "oversize", []byte("abc"))
	if _, e := i.Chunk("chat", u.ID, 0, make([]byte, MaxUploadChunk+1)); e == nil {
		t.Fatal("unbounded chunk")
	}
	if _, e := i.Chunk("chat", u.ID, 0, []byte("abcd")); e == nil {
		t.Fatal("oversize declared content")
	}
}
func TestUploadInterruptedChunkReconcilesOnlyKnownBytes(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(map[bool]string{true: "partial", false: "complete"}[partial], func(t *testing.T) {
			i, _ := uploadFixture(t)
			b := []byte("content")
			u := beginUpload(t, i, "crash", b)
			r, e := i.load("chat", u.ID)
			if e != nil {
				t.Fatal(e)
			}
			r.Pending = &pendingChunk{0, len(b), digest(b)}
			if e = i.save(r); e != nil {
				t.Fatal(e)
			}
			tail := b
			if partial {
				tail = b[:2]
			}
			if e = os.WriteFile(filepath.Join(i.dir, u.ID+".blob"), tail, 0600); e != nil {
				t.Fatal(e)
			}
			reopened := NewInbox(i.dir, i.db)
			s, e := reopened.Status("chat", u.ID)
			if e != nil || s.State != "reconcile_required" {
				t.Fatal(s, e)
			}
			if s, e = reopened.Chunk("chat", u.ID, 0, b); e != nil || s.Offset != int64(len(b)) {
				t.Fatal(s, e)
			}
			if _, e = reopened.Finish("chat", u.ID); e != nil {
				t.Fatal(e)
			}
		})
	}
	i, _ := uploadFixture(t)
	u := beginUpload(t, i, "unexpected", []byte("abc"))
	if e := os.WriteFile(filepath.Join(i.dir, u.ID+".blob"), []byte("x"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := i.Chunk("chat", u.ID, 0, []byte("abc")); e == nil {
		t.Fatal("unexplained bytes overwritten")
	}
}
func TestUploadSymlinkAndRace(t *testing.T) {
	i, dir := uploadFixture(t)
	u := beginUpload(t, i, "link", []byte("abc"))
	target := filepath.Join(dir, "outside")
	if e := os.WriteFile(target, []byte("safe"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(i.dir, u.ID+".blob")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, filepath.Join(i.dir, u.ID+".blob")); e != nil {
		t.Fatal(e)
	}
	if _, e := i.Chunk("chat", u.ID, 0, []byte("abc")); e == nil {
		t.Fatal("symlink followed")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "safe" {
		t.Fatal("outside mutated")
	}
	if _, e := i.Cancel("chat", u.ID); e != nil {
		t.Fatal(e)
	}
	u = beginUpload(t, i, "race", []byte("abc"))
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := i.Chunk("chat", u.ID, 0, []byte("abc")); e == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	s, e := i.Status("chat", u.ID)
	if e != nil || wins.Load() != 2 || s.Offset != 3 {
		t.Fatal("race duplicated bytes", s, e)
	}
}
func TestUploadQuotaIsDurable(t *testing.T) {
	i, _ := uploadFixture(t)
	// Reserving declared size limits disk consumption before bytes arrive.
	for n := range 4 {
		if _, e := i.Begin("chat", string(rune('a'+n)), "file.bin", MaxUploadSize, strings.Repeat("a", 64)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := NewInbox(i.dir, i.db).Begin("chat", "full", "file.bin", 1, strings.Repeat("a", 64)); e == nil {
		t.Fatal("restart bypassed quota")
	}
	keys, e := i.db.List(uploadPrefix)
	if e != nil {
		t.Fatal(e)
	}
	var r uploadRecord
	raw, _ := i.db.Get(keys[0])
	if e = json.Unmarshal(raw, &r); e != nil {
		t.Fatal(e)
	}
	if _, e = i.Cancel("chat", r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = i.Begin("chat", "freed", "file.bin", 1, strings.Repeat("a", 64)); e != nil {
		t.Fatal("cancel did not release quota", e)
	}
}

func TestUploadRejectsHardlinkAndSealedTampering(t *testing.T) {
	i, dir := uploadFixture(t)
	u := beginUpload(t, i, "hardlink", []byte("abc"))
	outside := filepath.Join(dir, "other-file")
	if e := os.Link(filepath.Join(i.dir, u.ID+".blob"), outside); e != nil {
		t.Fatal(e)
	}
	if _, e := i.Chunk("chat", u.ID, 0, []byte("abc")); e == nil {
		t.Fatal("hardlink outside inbox mutated")
	}
	i, _ = uploadFixture(t)
	u = beginUpload(t, i, "tamper", []byte("abc"))
	if _, e := i.Chunk("chat", u.ID, 0, []byte("abc")); e != nil {
		t.Fatal(e)
	}
	s, e := i.Finish("chat", u.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(s.Path, []byte("xyz"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = i.Status("chat", u.ID); e == nil {
		t.Fatal("changed sealed file still reported ready")
	}
}

func TestUploadFailedCancelKeepsReservedQuota(t *testing.T) {
	i, _ := uploadFixture(t)
	var first Upload
	for n := range 4 {
		u, e := i.Begin("chat", string(rune('a'+n)), "file.bin", MaxUploadSize, strings.Repeat("a", 64))
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			first = u
		}
	}
	path := filepath.Join(i.dir, first.ID+".blob")
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(path, "unknown"), []byte("protected"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := i.Cancel("chat", first.ID); e == nil {
		t.Fatal("nonempty foreign directory removed")
	}
	if _, e := i.Begin("chat", "new", "file", 1, strings.Repeat("a", 64)); e == nil {
		t.Fatal("failed cancellation released reserved bytes")
	}
}

func TestInboxDiskReserveAndExpiredUpload(t *testing.T) {
	const gib = uint64(1 << 30)
	const mib = uint64(1 << 20)
	if diskReserveOK(20*gib, 3*gib+16*mib, 64*mib) {
		t.Fatal("pending reservation exceeds free reserve")
	}
	if !diskReserveOK(20*gib, 3*gib+64*mib, 64*mib) {
		t.Fatal("exact reserved budget rejected")
	}
	if diskReserveOK(60*gib, 6*gib, 1) {
		t.Fatal("ten percent reserve consumed")
	}
	i, _ := uploadFixture(t)
	u := beginUpload(t, i, "ttl", []byte("abc"))
	i.now = func() time.Time { return u.ExpiresAt.Add(time.Second) }
	if s, e := i.Status("chat", u.ID); e != nil || s.State != "expired" {
		t.Fatal(s, e)
	}
	if _, e := i.Chunk("chat", u.ID, 0, []byte("abc")); e == nil {
		t.Fatal("expired upload renewed by writing")
	}
	if _, e := i.Cancel("chat", u.ID); e != nil {
		t.Fatal("cannot cancel expired owned partial", e)
	}
}

func TestInboxDiskReserveFailure(t *testing.T) {
	i, _ := uploadFixture(t)
	inboxStatfs = func(_ int, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 2 << 18}
		return nil
	}
	if _, err := i.Begin("chat", "disk", "file", 1, digest([]byte("x"))); err == nil || !strings.Contains(err.Error(), "disk reserve insufficient") {
		t.Fatal("low disk admitted", err)
	}
	inboxStatfs = func(int, *syscall.Statfs_t) error { return syscall.EIO }
	if _, err := i.Begin("chat", "disk", "file", 1, digest([]byte("x"))); err != syscall.EIO {
		t.Fatal("statfs error lost", err)
	}
}
