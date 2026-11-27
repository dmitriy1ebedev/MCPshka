package executor

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ProtectedImport struct {
	Version    int                `json:"version"`
	ApprovalID string             `json:"approval_id"`
	Repository RepositoryIdentity `json:"repository"`
	Candidate  string             `json:"candidate"`
	Target     string             `json:"target"`
	State      string             `json:"state"`
}

func protectedImportKey(id string) string { return "executor/protected-imports/" + id + ".json" }

// ImportProtectedIntegration imports immutable objects only, never refs.
// Git parsing runs as mcpshka, not root. A sealed intent precedes root writes;
// interruption may replay identical objects without repeating any ref effect.
func ImportProtectedIntegration(ctx context.Context, id string) (ProtectedImport, error) {
	// One fsync per object and directory: ~25 objects/s on H96 eMMC, so a
	// 4.5k-object history outlasted 120 s. Resume keeps retries cheap.
	ctx, cancel := context.WithTimeout(ctx, 600*time.Second)
	defer cancel()
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return ProtectedImport{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return ProtectedImport{}, err
	}
	var result ProtectedImport
	err = c.withIntegrationLock(ctx, a.Repository, func() error {
		if _, e := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, a.Repository, 0); e != nil {
			return ErrHold
		}
		if e := verifyProtectedRepositoryStorage(ctx, a.Repository); e != nil {
			return ErrHold
		}
		root, e := os.OpenRoot(a.Repository.CommonDir)
		if e != nil {
			return ErrHold
		}
		defer root.Close()
		if e := protectedImportSpace(root, uint64(protectedObjectBytes)+(384<<20)); e != nil {
			return e
		}
		intent := ProtectedImport{Version: 1, ApprovalID: a.ID, Repository: a.Repository, Candidate: a.Request.Commit, Target: a.Request.TargetHead, State: "IMPORT_INTENT"}
		raw, _ := json.Marshal(intent)
		fresh, e := c.db.PutIfAbsent(protectedImportKey(a.ID), raw)
		if e != nil {
			return e
		}
		if !fresh {
			prior, e := c.db.GetContext(ctx, protectedImportKey(a.ID))
			if e != nil || len(prior) > 4096 {
				return ErrHold
			}
			var old ProtectedImport
			d := json.NewDecoder(bytes.NewReader(prior))
			d.DisallowUnknownFields()
			if d.Decode(&old) != nil || d.Decode(new(any)) != io.EOF {
				return ErrHold
			}
			state := old.State
			old.State = "IMPORT_INTENT"
			if old != intent || (state != "IMPORT_INTENT" && state != "VERIFIED") {
				return ErrHold
			}
			if state == "VERIFIED" {
				if e := validateProtectedObjects(ctx, root, intent.Candidate); e != nil {
					return ErrHold
				}
				if e := validateProtectedObjects(ctx, root, intent.Target); e != nil {
					return ErrHold
				}
				result = intent
				result.State = "VERIFIED"
				return nil
			}
		}
		listed, e := gitSnapshotRead(ctx, a.Source.Repo, "rev-list", "--objects", "--no-object-names", intent.Candidate, intent.Target)
		if e != nil || len(listed) > protectedObjectCount*41 {
			return ErrHold
		}
		var oids []string
		seen := make(map[string]bool)
		for _, oid := range strings.Split(strings.TrimSuffix(string(listed), "\n"), "\n") {
			if !commitPattern.MatchString(oid) || seen[oid] || len(oids) >= protectedObjectCount {
				return ErrHold
			}
			seen[oid] = true
			oids = append(oids, oid)
		}
		if !seen[intent.Candidate] || !seen[intent.Target] {
			return ErrHold
		}
		cmd, e := protectedSourceBatch(ctx, a.Source.Repo)
		if e != nil {
			return ErrHold
		}
		cmd.Stdin = strings.NewReader(strings.Join(oids, "\n") + "\n")
		out, e := cmd.StdoutPipe()
		if e != nil {
			return ErrHold
		}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		if e = cmd.Start(); e != nil {
			return ErrHold
		}
		waited := false
		defer func() {
			if !waited {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				cmd.Wait()
			}
		}()
		if e := copyProtectedObjectBatch(ctx, root, oids, out); e != nil {
			return e
		}
		e = cmd.Wait()
		waited = true
		if e != nil {
			return ErrHold
		}
		if e := validateProtectedObjects(ctx, root, intent.Candidate); e != nil {
			return ErrHold
		}
		if e := validateProtectedObjects(ctx, root, intent.Target); e != nil {
			return ErrHold
		}
		if _, e := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, a.Repository, 0); e != nil {
			return ErrHold
		}
		intent.State = "VERIFIED"
		raw, _ = json.Marshal(intent)
		if e := c.db.Put(protectedImportKey(a.ID), raw); e != nil {
			return e
		}
		result = intent
		return nil
	})
	return result, err
}

func protectedSourceBatch(ctx context.Context, repo string) (*exec.Cmd, error) {
	if !validSource(&SourceSnapshot{Repo: repo, Commit: strings.Repeat("1", 40), Package: "internal/executor", Digest: strings.Repeat("1", 64)}) {
		return nil, ErrHold
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/git", "--no-replace-objects", "--no-lazy-fetch", "-c", "safe.directory="+repo, "-c", "protocol.allow=never", "-C", repo, "cat-file", "--batch")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1"}
	if os.Geteuid() == 0 {
		account, e := user.Lookup("mcpshka")
		credential, e := snapshotGitCredential(account, e)
		if e != nil {
			return nil, ErrHold
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential, Setpgid: true}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// Private stream boundary permits isolated real-filesystem tests without live
// root authority. Object IDs come from a bounded pinned-source enumeration.
func copyProtectedObjectBatch(ctx context.Context, root *os.Root, oids []string, input io.Reader) error {
	if root == nil || len(oids) == 0 || len(oids) > protectedObjectCount {
		return ErrHold
	}
	r := bufio.NewReaderSize(contextObjectReader{Ctx: ctx, R: io.LimitReader(input, protectedObjectBytes+int64(protectedObjectCount*100))}, 512)
	remaining := protectedObjectBytes
	for _, oid := range oids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !commitPattern.MatchString(oid) {
			return ErrHold
		}
		lineBytes, err := r.ReadSlice('\n')
		line := string(lineBytes)
		if err != nil || len(line) > 128 {
			return ErrHold
		}
		parts := strings.Split(strings.TrimSuffix(line, "\n"), " ")
		if len(parts) != 3 || parts[0] != oid || (parts[1] != "blob" && parts[1] != "tree" && parts[1] != "commit") {
			return ErrHold
		}
		n, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != parts[2] || n > 64<<20 || n > remaining {
			return ErrHold
		}
		// Resume: an already published, verified object of the same kind and
		// size is not rewritten. The stream is still hashed against the oid,
		// so a skipped object cannot hide a corrupted batch.
		if kind, _, size, err := readProtectedObject(ctx, root, oid, protectedObjectBytes); err == nil && kind == parts[1] && size == n {
			h := sha1.New()
			io.WriteString(h, parts[1]+" "+parts[2]+"\x00")
			copied, copyErr := io.CopyBuffer(h, io.LimitReader(r, n), make([]byte, 32768))
			newline, nlErr := r.ReadByte()
			if copyErr != nil || copied != n || hex.EncodeToString(h.Sum(nil)) != oid || nlErr != nil || newline != '\n' {
				return ErrHold
			}
			remaining -= n
			continue
		}
		if err := protectedImportSpace(root, uint64(remaining)+(384<<20)); err != nil {
			return err
		}
		remaining -= n
		dir := "objects/" + oid[:2]
		if err := root.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return ErrHold
		}
		tmp := dir + "/.import-" + oid[2:]
		// A prior partial temp is private root storage, never an authoritative
		// object. Recreate this exact scratch file; published objects are immutable.
		if err := root.Remove(tmp); err != nil && !os.IsNotExist(err) {
			return ErrHold
		}
		f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return ErrHold
		}
		z := zlib.NewWriter(f)
		h := sha1.New()
		header := parts[1] + " " + parts[2] + "\x00"
		_, werr := io.WriteString(io.MultiWriter(z, h), header)
		copied, copyErr := io.CopyBuffer(io.MultiWriter(z, h), io.LimitReader(r, n), make([]byte, 32768))
		zerr := z.Close()
		syncErr := f.Sync()
		closeErr := f.Close()
		if werr != nil || copyErr != nil || copied != n || zerr != nil || syncErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != oid {
			root.Remove(tmp)
			return ErrHold
		}
		newline, err := r.ReadByte()
		if err != nil || newline != '\n' {
			root.Remove(tmp)
			return ErrHold
		}
		d, err := root.Open(dir)
		if err != nil {
			return ErrHold
		}
		err = unix.Renameat2(int(d.Fd()), ".import-"+oid[2:], int(d.Fd()), oid[2:], unix.RENAME_NOREPLACE)
		if err != nil && !os.IsExist(err) {
			d.Close()
			root.Remove(tmp)
			return ErrHold
		}
		if os.IsExist(err) {
			if err := root.Remove(tmp); err != nil {
				d.Close()
				return ErrHold
			}
		}
		syncErr = d.Sync()
		closeErr = d.Close()
		if syncErr != nil || closeErr != nil {
			return ErrHold
		}
		if _, _, _, err := readProtectedObject(ctx, root, oid, protectedObjectBytes); err != nil {
			return ErrHold
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return ErrHold
	}
	objects, err := root.Open("objects")
	if err != nil {
		return ErrHold
	}
	syncErr := objects.Sync()
	closeErr := objects.Close()
	if syncErr != nil || closeErr != nil {
		return ErrHold
	}
	return nil
}

// Import peak includes object data, bounded scratch, filesystem allocation and
// SQLite overhead. Recheck on the pinned destination as independent jobs may
// consume disk after the initial admission check (statfs swapped by tests).
var protectedImportStatfs = syscall.Fstatfs

func protectedImportSpace(root *os.Root, peak uint64) error {
	f, err := root.Open(".")
	if err != nil {
		return ErrHold
	}
	defer f.Close()
	var st syscall.Statfs_t
	if protectedImportStatfs(int(f.Fd()), &st) != nil || st.Bsize <= 0 {
		return ErrHold
	}
	block := uint64(st.Bsize)
	// Overflow is checked first: the reserve math runs only on sane counts.
	if st.Blocks > (^uint64(0))/block || st.Bavail > (^uint64(0))/block || !protectedImportReserve(st.Blocks*block, st.Bavail*block, peak) {
		return ErrHold
	}
	return nil
}
func protectedImportReserve(total, available, peak uint64) bool {
	reserve := uint64(3) << 30
	if total/10 > reserve {
		reserve = total / 10
	}
	return total > 0 && available <= total && available >= reserve && peak <= available-reserve
}
