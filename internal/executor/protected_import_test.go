package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestProtectedImportRealBatchAndReplay(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "objects"), 0700); err != nil {
		t.Fatal(err)
	}
	commit, _ := protectedTestCommit(t, source)
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceRoot.Close()
	var oids []string
	var stream bytes.Buffer
	err = filepath.WalkDir(filepath.Join(source, "objects"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		oid := filepath.Base(filepath.Dir(path)) + d.Name()
		kind, payload, n, err := readProtectedObject(context.Background(), sourceRoot, oid, protectedObjectBytes)
		if err != nil {
			return err
		}
		// The fixture blob body is streamed in production; the test supplies its known bytes.
		if kind == "blob" {
			payload = []byte("verified source\n")
		}
		oids = append(oids, oid)
		fmt.Fprintf(&stream, "%s %s %d\n", oid, kind, n)
		stream.Write(payload)
		stream.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(born.Repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := 0; i < 2; i++ {
		if i == 1 {
			// Resume must verify published objects without rewriting them:
			// read-only object directories refuse any new temp file.
			for _, oid := range oids {
				dir := filepath.Join(born.Repository.CommonDir, "objects", oid[:2])
				if err := os.Chmod(dir, 0500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(dir, 0700)
			}
		}
		if err := copyProtectedObjectBatch(context.Background(), root, oids, bytes.NewReader(stream.Bytes())); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			for _, oid := range oids {
				os.Chmod(filepath.Join(born.Repository.CommonDir, "objects", oid[:2]), 0700)
			}
		}
		if err := validateProtectedObjects(context.Background(), root, commit); err != nil {
			t.Fatal(err)
		}
		if err := inspectProtectedRepository(context.Background(), born, uint32(os.Geteuid())); err != nil {
			t.Fatalf("import left unsealed storage: %v", err)
		}
	}
}

func TestProtectedImportRejectsMalformedBatchAndPreservesObjects(t *testing.T) {
	for _, bad := range []string{"wrong-id", "hash-mismatch", "huge-header", "truncated-body", "extra-output", "oversized-body"} {
		t.Run(bad, func(t *testing.T) {
			c, parent := protectedBirthFixture(t)
			born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
			if err != nil {
				t.Fatal(err)
			}
			oid := writeProtectedTestObject(t, born.Repository.CommonDir, "blob", []byte("verified source\n"))
			path := filepath.Join(born.Repository.CommonDir, "objects", oid[:2], oid[2:])
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw := oid + " blob 16\nverified source\n\n"
			switch bad {
			case "wrong-id":
				raw = strings.Repeat("1", 40) + " blob 16\nverified source\n\n"
			case "hash-mismatch":
				raw = oid + " blob 16\naltered content!\n\n"
			case "huge-header":
				raw = strings.Repeat("x", 1024) + "\n"
			case "truncated-body":
				raw = oid + " blob 16\nshort"
			case "extra-output":
				raw += "unexpected"
			case "oversized-body":
				raw = oid + " blob 67108865\n"
			}
			root, err := os.OpenRoot(born.Repository.CommonDir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := copyProtectedObjectBatch(context.Background(), root, []string{oid}, strings.NewReader(raw)); !errors.Is(err, ErrHold) {
				t.Fatalf("%s accepted: %v", bad, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("published object changed: %v", err)
			}
		})
	}
}

func TestProtectedImportRootBoundaryRejectsDaemon(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires daemon identity")
	}
	if _, err := ImportProtectedIntegration(context.Background(), strings.Repeat("1", 64)); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
}

func TestProtectedImportReserveAdmission(t *testing.T) {
	peak := uint64(protectedObjectBytes) + (384 << 20)
	for _, total := range []uint64{20 << 30, 100 << 30} {
		reserve := uint64(3) << 30
		if total/10 > reserve {
			reserve = total / 10
		}
		if protectedImportReserve(total, reserve+peak-1, peak) {
			t.Fatal("import would consume reserve")
		}
		if !protectedImportReserve(total, reserve+peak, peak) {
			t.Fatal("exact safe headroom rejected")
		}
		if protectedImportReserve(total, reserve-1, 0) {
			t.Fatal("already below reserve admitted")
		}
	}
	if protectedImportReserve(0, 0, 0) || protectedImportReserve(1, 2, 0) || protectedImportReserve(^uint64(0), ^uint64(0), ^uint64(0)) {
		t.Fatal("invalid or overflowing admission")
	}
}

func TestProtectedImportCancellationPreservesPublishedObjects(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(born.Repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyProtectedObjectBatch(ctx, root, []string{strings.Repeat("1", 40)}, strings.NewReader("unused")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(born.Repository.CommonDir, "objects"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("cancelled import mutated storage: %v %+v", err, entries)
	}
}

func TestProtectedImportSpaceDiskReserveFailure(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	previous := protectedImportStatfs
	t.Cleanup(func() { protectedImportStatfs = previous })
	for _, fault := range []error{nil, syscall.EIO} {
		protectedImportStatfs = func(_ int, st *syscall.Statfs_t) error {
			*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 2 << 18}
			return fault
		}
		if err := protectedImportSpace(root, 1<<20); !errors.Is(err, ErrHold) {
			t.Fatal("low or unknown disk admitted", err)
		}
	}
}
