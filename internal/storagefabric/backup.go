package storagefabric

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type BundleManifest struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
	Head       string `json:"head"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Dirty      bool   `json:"working_tree_excluded"`
}
type Manifest struct {
	Version int              `json:"version"`
	Created time.Time        `json:"created"`
	Bundles []BundleManifest `json:"bundles"`
}

func validateRepository(p string) (string, error) {
	if filepath.Clean(p) != p || filepath.Dir(p) != "/root/repos" || strings.ContainsAny(filepath.Base(p), ":\\") {
		return "", ErrInvalid
	}
	real, e := filepath.EvalSymlinks(p)
	if e != nil || real != p {
		return "", ErrInvalid
	}
	st, e := os.Stat(p)
	if e != nil || !st.IsDir() {
		return "", ErrInvalid
	}
	return p, nil
}
func (e Engine) backupPlan(ctx context.Context, r Request) (Result, error) {
	if len(r.Repositories) == 0 || len(r.Repositories) > 16 {
		return Result{}, ErrInvalid
	}
	manifest := Manifest{Version: 1, Created: time.Now().UTC()}
	seen := map[string]bool{}
	for _, p := range r.Repositories {
		real, err := validateRepository(p)
		if err != nil || seen[p] {
			return Result{}, ErrInvalid
		}
		seen[p] = true
		root, err := git(ctx, real, "rev-parse", "--show-toplevel")
		if err != nil || strings.TrimSpace(string(root)) != real {
			return Result{}, ErrInvalid
		}
		head, err := git(ctx, real, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		dirty, err := git(ctx, real, "status", "--porcelain", "--untracked-files=no")
		if err != nil {
			return Result{}, err
		}
		manifest.Bundles = append(manifest.Bundles, BundleManifest{Name: filepath.Base(real) + ".bundle", Repository: real, Head: strings.TrimSpace(string(head)), Dirty: len(dirty) > 0})
	}
	raw, _ := json.Marshal(manifest)
	return Result{Trust: "UNTRUSTED", State: "planned", Entries: raw, Message: "Git refs and committed objects only; working/untracked files excluded. Plain archive (manifest.json + git bundles; the MCPshka state backup is the encrypted one), 256 MiB snapshot limit, 768 MiB temporary peak. No mirror deletion."}, nil
}
func git(ctx context.Context, repo string, args ...string) ([]byte, error) {
	prefix := []string{"--no-optional-locks", "-c", "safe.directory=" + repo, "-c", "core.hooksPath=/dev/null", "-C", repo}
	return command(ctx, "/usr/bin/git", append(prefix, args...)...)
}
func (e Engine) key() ([]byte, error) {
	p := filepath.Join(e.Vault.Dir, "backup.key")
	if b, err := privateFile(p); err == nil {
		if len(b) != 32 {
			return nil, ErrInvalid
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	existing, err := os.ReadDir(filepath.Join(e.Dir, "backups"))
	if err != nil || len(existing) > 0 {
		return nil, errors.New("backup key missing; existing ciphertext requires original key")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return nil, err
	}
	return b, ce
}
func (e Engine) createBackup(ctx context.Context, op *operation) error {
	planned, err := e.backupPlan(ctx, op.Request)
	if err != nil {
		return err
	}
	if err = supported(op.Request.Provider); err != nil {
		return err
	}
	work := filepath.Join(e.Dir, "staging", op.Result.ID)
	if err = os.Mkdir(work, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(work) // only this job's newly created bounded scratch
	var manifest Manifest
	if err = json.Unmarshal(planned.Entries, &manifest); err != nil {
		return err
	}
	if err = e.checkpoint(op, "snapshot_planned", nil); err != nil {
		return err
	}
	var total int64
	for i, b := range manifest.Bundles {
		file := filepath.Join(work, b.Name)
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		// git bundle writes stdout directly to a bounded file, never to a tool/log.
		cmd := exec.CommandContext(ctx, "/usr/bin/git", "--no-optional-locks", "-c", "safe.directory="+b.Repository, "-c", "core.hooksPath=/dev/null", "-C", b.Repository, "bundle", "create", "-", "--all")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1"}
		sink := &budgetWriter{w: f, left: MaxFileBytes - total - 65536}
		cmd.Stdout = sink
		cmd.Stderr = io.Discard
		err = cmd.Run()
		ce := f.Close()
		if err != nil || ce != nil {
			return errors.New("git bundle failed or exceeds budget")
		}
		digest, size, err := hashFile(file)
		if err != nil {
			return err
		}
		// The bundled HEAD must match the planned snapshot, including concurrent ref changes.
		heads, err := git(ctx, b.Repository, "bundle", "list-heads", file, "HEAD")
		if err != nil || !strings.HasPrefix(string(heads), b.Head+" ") {
			return errors.New("repository changed while snapshotting")
		}
		manifest.Bundles[i].SHA256 = digest
		manifest.Bundles[i].Size = size
		total += size
	}
	blob := filepath.Join(e.Dir, "backups", op.Result.ID+".tar")
	f, err := os.OpenFile(blob, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// Plain tar: repositories hold committed code only (the encrypted state
	// backup is separate), and the owner opens it with tar + git clone.
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		tw := tar.NewWriter(pw)
		raw, _ := json.Marshal(manifest)
		err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw))})
		if err == nil {
			_, err = tw.Write(raw)
		}
		for _, b := range manifest.Bundles {
			if err != nil {
				break
			}
			err = tw.WriteHeader(&tar.Header{Name: b.Name, Mode: 0600, Size: b.Size})
			if err == nil {
				in, openErr := os.Open(filepath.Join(work, b.Name))
				if openErr != nil {
					err = openErr
					break
				}
				_, err = io.Copy(tw, in)
				in.Close()
			}
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
		done <- err
	}()
	_, err = io.Copy(&budgetWriter{w: f, left: MaxFileBytes}, pr)
	pr.CloseWithError(err)
	writerErr := <-done
	if err == nil {
		err = writerErr
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	digest, size, err := hashFile(blob)
	if err != nil {
		return err
	}
	op.Result.SHA256 = digest
	op.Result.Size = size
	op.Result.Format = "tar"
	op.Result.Message = "plain archive (manifest.json + git bundles) uploaded; backup_verify must download and compare before acceptance"
	// Commit expected hash BEFORE external I/O so lost upload replies are reconcilable.
	if err = e.checkpoint(op, "archive_sealed", []string{"local_hash_size", "git_head_pinned"}); err != nil {
		return err
	}
	_, err = e.rclone(ctx, op.Request.Provider, "copyto", blob, op.Request.Provider+":MCPshka/backups/"+op.Result.ID+".tar", "--immutable")
	if err != nil {
		return err
	}
	return e.checkpoint(op, "uploaded", []string{"local_hash_size", "git_head_pinned", "upload_ack"})
}

type budgetWriter struct {
	w    io.Writer
	left int64
}

func (w *budgetWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		return 0, errors.New("backup budget exceeded")
	}
	n, e := w.w.Write(b)
	w.left -= int64(n)
	return n, e
}
func (e Engine) verifyBackup(ctx context.Context, op *operation) error {
	// Destination carries the original backup ID; ID names this verification job.
	original, err := e.load(op.Request.Destination, op.Request.Owner)
	if err != nil {
		return err
	}
	if original.Result.Workflow != nil && original.Result.Workflow.Schema != BackupSchema {
		return errors.New("backup contract incompatible: HOLD")
	}
	if original.Request.Action != "backup_create" || original.Result.SHA256 == "" {
		return ErrInvalid
	}
	if err = e.checkpoint(op, "download_validate", nil); err != nil {
		return err
	}
	if err = diskOK(e.Dir, MaxFileBytes); err != nil {
		return err
	}
	downloaded := filepath.Join(e.Dir, "staging", op.Result.ID+".verify")
	if _, err = os.Lstat(downloaded); !os.IsNotExist(err) {
		return ErrInvalid
	}
	defer os.Remove(downloaded)
	ext := ".enc"
	if original.Result.Format == "tar" {
		ext = ".tar"
	}
	_, err = e.rclone(ctx, original.Request.Provider, "copyto", original.Request.Provider+":MCPshka/backups/"+original.Result.ID+ext, downloaded, "--immutable", "--max-transfer=256Mi")
	if err != nil {
		return err
	}
	digest, size, err := hashFile(downloaded)
	if err != nil {
		return err
	}
	if digest != original.Result.SHA256 || size != original.Result.Size {
		return errors.New("downloaded backup SHA-256 mismatch")
	}
	if original.Result.Format == "tar" {
		return e.acceptVerified(&original, op, downloaded, digest, size, "downloaded archive hash, manifest and every bundle hash verified")
	}
	key, err := privateFile(filepath.Join(e.Vault.Dir, "backup.key"))
	if err != nil || len(key) != 32 {
		return errors.New("original local backup key required")
	}
	f, err := os.Open(downloaded)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	decrypted := make(chan error, 1)
	go func() { err := Decrypt(pw, f, key); pw.CloseWithError(err); decrypted <- err }()
	manifest, manifestErr := DecodeManifest(pr)
	// Drain so authenticated termination is checked as well as tar contents.
	if manifestErr == nil {
		_, manifestErr = io.Copy(io.Discard, pr)
	}
	pr.CloseWithError(manifestErr)
	decryptErr := <-decrypted
	if manifestErr != nil {
		return manifestErr
	}
	if decryptErr != nil {
		return decryptErr
	}
	original.Result.Entries, _ = json.Marshal(manifest)
	original.Result.State = "verified"
	if err = e.checkpoint(&original, "verified", []string{"download_hash_aead_manifest"}); err != nil {
		return err
	}
	op.Result.SHA256 = digest
	op.Result.Size = size
	op.Result.Message = "downloaded ciphertext hash and AEAD termination verified"
	return e.checkpoint(op, "verified", []string{"download_hash_aead_manifest"})
}
func (e Engine) acceptVerified(original, op *operation, path, digest string, size int64, message string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	manifest, err := DecodeManifest(f)
	if err != nil {
		return err
	}
	original.Result.Entries, _ = json.Marshal(manifest)
	original.Result.State = "verified"
	if err = e.save(*original); err != nil {
		return err
	}
	op.Result.SHA256, op.Result.Size, op.Result.Message = digest, size, message
	return nil
}

func (e Engine) restorePlan(ctx context.Context, r Request) (Result, error) {
	original, err := e.load(r.ID, r.Owner)
	if err != nil {
		return Result{}, err
	}
	if original.Request.Action != "backup_create" || original.Result.State != "verified" {
		return Result{}, errors.New("backup_verify required before staging restore plan")
	}
	return Result{Trust: "UNTRUSTED", ID: r.ID, State: "planned", Workflow: original.Result.Workflow, Entries: original.Result.Entries, SHA256: original.Result.SHA256, LocalPath: filepath.Join(e.Dir, "staging", "restore-"+r.ID), Message: restoreSteps(original.Result.Format)}, nil
}

func restoreSteps(format string) string {
	if format == "tar" {
		return "Download the .tar, compare SHA-256, validate manifest and bundle hashes, git bundle verify, then git clone each bundle into this new staging directory only. No production write authorized."
	}
	return "Download encrypted backup, compare SHA-256, decrypt with local backup.key, validate manifest and bundle hashes, git bundle verify, then clone into this new staging directory only. No production write authorized."
}

// DecodeManifest validates an independently decrypted archive before any extraction.
func DecodeManifest(reader io.Reader) (Manifest, error) {
	tr := tar.NewReader(reader)
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" || h.Size > 65536 || h.Size < 1 {
		return Manifest{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(tr, 65537))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || m.Version != 1 || len(m.Bundles) == 0 || len(m.Bundles) > 16 {
		return Manifest{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, b := range m.Bundles {
		if filepath.Base(b.Name) != b.Name || !strings.HasSuffix(b.Name, ".bundle") || seen[b.Name] || b.Size < 1 || b.Size > MaxFileBytes {
			return Manifest{}, ErrInvalid
		}
		seen[b.Name] = true
		h, err = tr.Next()
		if err != nil || h.Name != b.Name || h.Typeflag != tar.TypeReg || h.Size != b.Size {
			return Manifest{}, ErrInvalid
		}
		digest := sha256.New()
		n, err := io.Copy(digest, tr)
		if err != nil || n != b.Size || hex.EncodeToString(digest.Sum(nil)) != b.SHA256 {
			return Manifest{}, errors.New("bundle manifest mismatch")
		}
	}
	if _, err = tr.Next(); err != io.EOF {
		return Manifest{}, ErrInvalid
	}
	return m, nil
}
