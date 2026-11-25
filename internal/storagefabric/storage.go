// Package storagefabric is the fixed root-broker storage capability.
// Remote bytes are UNTRUSTED; credentials never cross its response boundary.
package storagefabric

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const VaultDir = "/var/lib/mcpshka-vault/storage"
const StateDir = "/var/lib/mcpshka-storage"
const Worker = "/usr/local/libexec/mcpshka-broker"
const Rclone = "/usr/local/bin/rclone"
const MaxFileBytes = int64(256 << 20)

// DocumentsDir receives cloud downloads for the document tools. systemd
// creates it 0711 (CacheDirectory): no listing, and each download sits in a
// folder named by its random 128-bit operation id, so only the caller that
// holds the id (the MCPshka service) can read the file.
const DocumentsDir = "/var/cache/mcpshka-documents"

// documentSource is a result of the document tools: <run id>/<file name>.
var documentSource = regexp.MustCompile(`^/var/lib/mcpshka/documents/[a-f0-9]{16}/[^/]{1,128}$`)

var ErrUnsupported = errors.New("UNSUPPORTED: official iCloud Drive host access not established; Apple web sessions disabled")
var ErrUnknown = errors.New("storage effect UNKNOWN; reconcile before retry")
var ErrInvalid = errors.New("invalid storage request")
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Request struct {
	ArtifactSHA256 string   `json:"artifact_sha256,omitempty"`
	ArtifactSize   int64    `json:"artifact_size,omitempty"`
	Action         string   `json:"action"`
	Provider       string   `json:"provider,omitempty"`
	Path           string   `json:"path,omitempty"`
	Destination    string   `json:"destination,omitempty"`
	ArtifactPath   string   `json:"artifact_path,omitempty"`
	ID             string   `json:"id,omitempty"`
	Key            string   `json:"key,omitempty"`
	Owner          string   `json:"owner"`
	Token          string   `json:"token,omitempty"`
	Repositories   []string `json:"repositories,omitempty"`
}
type Provider struct {
	ID            string `json:"id"`
	Backend       string `json:"backend"`
	State         string `json:"state"`
	Authenticated string `json:"authenticated"`
	Namespace     string `json:"namespace"`
	Authorize     string `json:"authorize,omitempty"`
	// Note is what the owner should know about this credential.
	Note         string   `json:"note,omitempty"`
	Capabilities []string `json:"capabilities"`
}
type Result struct {
	Workflow  *Workflow       `json:"workflow,omitempty"`
	Trust     string          `json:"trust"`
	ID        string          `json:"id,omitempty"`
	State     string          `json:"state"`
	Providers []Provider      `json:"providers,omitempty"`
	Entries   json.RawMessage `json:"entries,omitempty"`
	SHA256    string          `json:"sha256,omitempty"`
	Size      int64           `json:"size,omitempty"`
	LocalPath string          `json:"local_path,omitempty"`
	Message   string          `json:"message,omitempty"`
	// Format of a repository backup: "tar" (plain manifest + git bundles,
	// owner decision 26.10: code must be easy to open) or empty for the
	// older AEAD-encrypted ".enc" archives.
	Format string `json:"format,omitempty"`
}
type StorageProvider interface {
	Do(context.Context, Request) (Result, error)
}

func supported(p string) error {
	switch p {
	case "drive", "yandex", "dropbox", "onedrive":
		return nil
	case "iclouddrive":
		return ErrUnsupported
	}
	return ErrInvalid
}
func relative(p string, empty bool) bool {
	if p == "" {
		return empty
	}
	return len(p) <= 1024 && path.Clean(p) == p && !path.IsAbs(p) && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, ":\\") && !strings.ContainsFunc(p, unicode.IsControl) && !strings.HasPrefix(p, "-") && !strings.HasPrefix(p, "MCPshka/.trash") && !strings.HasPrefix(p, "MCPshka/backups")
}
func Remote(provider, p string, empty bool) (string, error) {
	if e := supported(provider); e != nil {
		return "", e
	}
	if !relative(p, empty) {
		return "", ErrInvalid
	}
	// The whole cloud, as the owner asked (09.11): any file or folder. Still
	// no overwrite (--immutable), no purge — trash moves into MCPshka/.trash.
	return provider + ":" + p, nil
}
func Command(r Request, local string) ([]string, error) {
	src, e := Remote(r.Provider, r.Path, r.Action == "list" || r.Action == "mkdir")
	if e != nil {
		return nil, e
	}
	if (r.Action == "move" || r.Action == "rename" || r.Action == "trash") && r.Path == "MCPshka" {
		return nil, ErrInvalid // the folder holding .trash and backups stays put
	}
	var args []string
	switch r.Action {
	case "list":
		args = []string{"lsjson", src, "--max-depth=1"}
	case "stat":
		args = []string{"lsjson", src, "--stat"}
	case "mkdir":
		args = []string{"mkdir", src}
	case "move", "rename":
		dst, e := Remote(r.Provider, r.Destination, false)
		if e != nil {
			return nil, e
		}
		if r.Path == r.Destination {
			return nil, ErrInvalid
		}
		args = []string{"moveto", src, dst, "--immutable"}
	case "trash":
		if !idPattern.MatchString(r.ID) {
			return nil, ErrInvalid
		}
		args = []string{"moveto", src, r.Provider + ":MCPshka/.trash/" + r.ID + "/" + path.Base(r.Path), "--immutable"}
	case "upload":
		args = []string{"copyto", local, src, "--immutable"}
	case "download":
		args = []string{"copyto", src, local, "--immutable", "--max-transfer=256Mi"}
	default:
		return nil, ErrInvalid
	}
	return args, nil
}
func privateDir(dir string) error {
	st, e := os.Lstat(dir)
	if e != nil {
		return errors.New("storage private directory unavailable")
	}
	u, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || !ok || u.Uid != uint32(os.Geteuid()) {
		return errors.New("storage directory must be private and owned")
	}
	return nil
}
func privateFile(p string) ([]byte, error) {
	st, e := os.Lstat(p)
	if e != nil {
		return nil, e
	}
	u, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || !ok || u.Nlink != 1 || u.Uid != uint32(os.Geteuid()) || st.Size() > 65536 {
		return nil, errors.New("unsafe storage file")
	}
	return os.ReadFile(p)
}
func atomicFile(p string, b []byte) error {
	dir := filepath.Dir(p)
	if e := privateDir(dir); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".write-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(name, p); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

type Engine struct {
	Vault Vault
	Dir   string
}

func providerLock(dir, provider string) (*os.File, error) {
	if e := supported(provider); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, provider+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("cloud busy; credential refresh serialized")
	}
	return f, nil
}
func (e Engine) providers() Result {
	out := Result{Trust: "UNTRUSTED", State: "observed"}
	for _, p := range []string{"drive", "yandex", "dropbox", "onedrive", "iclouddrive"} {
		v := Provider{ID: p, Backend: p, State: "needs_owner_auth", Authenticated: "unknown", Namespace: "", Authorize: "storage_connect provider=" + p, Capabilities: []string{"list", "stat", "mkdir", "upload", "download", "move", "rename", "trash"}}
		if p == "iclouddrive" {
			v.State = "UNSUPPORTED"
			v.Authorize = ""
			v.Capabilities = []string{}
		} else if cfg, err := e.Vault.Config(p); err == nil {
			v.State = "credential_present"
			v.Authenticated = "unverified"
			v.Note = "storage_auth_status provider=" + p + " checks it live"
			if raw, _ := privateFile(cfg); p == "drive" && !bytes.Contains(raw, []byte("\nclient_id = ")) {
				v.Note = "uses rclone's shared Google client: rate-limited and retired during 2026 — reconnect with storage_connect provider=drive (own app, one time)"
			}
		}
		if p == "onedrive" {
			v.Authorize = "owner page «Подключения» → OneDrive"
		}
		out.Providers = append(out.Providers, v)
	}
	return out
}
func diskOK(dir string, need int64) error {
	var st syscall.Statfs_t
	if need < 0 || syscall.Statfs(dir, &st) != nil {
		return errors.New("storage disk probe failed")
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	reserve := max(uint64(3<<30), total/10)
	if free < reserve || uint64(need) > free-reserve {
		return errors.New("storage disk reserve insufficient")
	}
	return nil
}

type capBuffer struct {
	bytes.Buffer
	max int
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, errors.New("storage output budget exceeded")
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8"}
	var buf, errBuf capBuffer
	buf.max, errBuf.max = 60000, 4096
	cmd.Stdout = &buf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, &commandError{reason: exitReason(bin, cmd.ProcessState.ExitCode(), rcloneReason(errBuf.String()))}
	}
	return buf.Bytes(), nil
}

func (e Engine) rclone(ctx context.Context, provider string, args ...string) ([]byte, error) {
	lock, err := providerLock(e.Vault.Dir, provider)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	cfg, err := e.Vault.Config(provider)
	if err != nil {
		return nil, err
	}
	flags := []string{"--config=" + cfg, "--checkers=1", "--transfers=1", "--buffer-size=1Mi", "--multi-thread-streams=0", "--bwlimit=8Mi", "--retries=1", "--low-level-retries=1", "--contimeout=10s", "--timeout=60s", "--log-level=ERROR", "--stats=0", "--max-backlog=1000", "--max-transfer=256Mi", "--drive-skip-shortcuts=true"}
	if provider == "drive" {
		// rclone's pacer retries 403 rateLimitExceeded/429 itself; one
		// low-level try turned every throttle into a failed op (MCP-165).
		flags = append(flags, "--low-level-retries=6", "--tpslimit=4", "--drive-pacer-min-sleep=200ms", "--drive-pacer-burst=20")
	}
	out, err := command(ctx, Rclone, append(flags, args...)...)
	if err != nil {
		reason := failureReason(err)
		if definiteRefusal(reason) {
			return nil, &commandError{reason: reason}
		}
		return nil, &rcloneFailure{reason: reason}
	}
	return out, nil
}

type operation struct {
	Request     Request `json:"request"`
	Result      Result  `json:"result"`
	Fingerprint string  `json:"fingerprint"`
}

func (e Engine) opPath(id string) string { return filepath.Join(e.Dir, id+".json") }
func (e Engine) load(id, owner string) (operation, error) {
	if !idPattern.MatchString(id) {
		return operation{}, ErrInvalid
	}
	raw, err := privateFile(e.opPath(id))
	if err != nil {
		return operation{}, errors.New("storage operation unavailable")
	}
	var op operation
	if json.Unmarshal(raw, &op) != nil || op.Request.Owner != owner {
		return operation{}, errors.New("storage operation unavailable")
	}
	return op, nil
}
func (e Engine) save(op operation) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return atomicFile(e.opPath(op.Result.ID), raw)
}
func UnitArgs(id string, worker bool) []string {
	name := "mcpshka-storage-" + id
	args := []string{"--unit=" + name, "--quiet", "--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077", "--property=RuntimeMaxSec=1800", "--property=MemoryMax=256M", "--property=CPUQuota=50%", "--property=TasksMax=24", "--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes", "--property=ProtectSystem=strict", "--property=ProtectHome=read-only", "--property=ReadWritePaths=" + VaultDir + " " + StateDir, "--property=CacheDirectory=mcpshka-documents", "--property=CacheDirectoryMode=0711", "--property=CapabilityBoundingSet=CAP_DAC_READ_SEARCH", "--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6", "--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes", "--property=RestrictNamespaces=yes", "--property=StandardError=null"}
	if worker {
		return append(args, "--no-block", Worker, "storage-job", id)
	}
	// Short broker calls get their request on stdin, never in argv/journal.
	return append(args, "--collect", "--wait", "--pipe", Worker, "storage-request")
}
func (e Engine) Do(ctx context.Context, r Request) (Result, error) {
	empty := Result{Trust: "UNTRUSTED"}
	if r.Owner == "" || len(r.Owner) > 256 {
		return empty, ErrInvalid
	}
	switch r.Action {
	case "health":
		h, err := e.Health(ctx, time.Now().UTC())
		raw, _ := json.Marshal(h)
		return Result{Trust: "UNTRUSTED", State: h.State, Entries: raw}, err
	case "providers", "auth_status":
		out := e.providers()
		if r.Action == "auth_status" && r.Provider != "" {
			// One live read proves the credential; presence alone does not.
			if err := supported(r.Provider); err != nil {
				return empty, err
			}
			for i := range out.Providers {
				v := &out.Providers[i]
				if v.ID != r.Provider || v.State != "credential_present" {
					continue
				}
				if _, err := e.rclone(ctx, r.Provider, "lsjson", r.Provider+":", "--max-depth=1"); err != nil {
					v.Authenticated, v.State, v.Note = "false", "credential_rejected", reasonOf(err)
				} else {
					v.Authenticated, v.State = "true", "connected"
				}
			}
		}
		return out, nil
	case "auth_accept":
		err := e.Vault.Accept(r.Provider, r.Token)
		return Result{Trust: "UNTRUSTED", State: "credential_present_unverified"}, err
	case "status", "backup_status":
		op, err := e.load(r.ID, r.Owner)
		if err != nil {
			return empty, err
		}
		e.settle(ctx, &op)
		return op.Result, nil
	case "list", "stat":
		args, err := Command(r, "")
		if err != nil {
			return empty, err
		}
		raw, err := e.rclone(ctx, r.Provider, args...)
		if err != nil {
			// A read has no effect to reconcile: say why it failed.
			return empty, fmt.Errorf("storage %s failed: %s", r.Action, reasonOf(err))
		}
		if !json.Valid(raw) {
			return empty, errors.New("invalid remote metadata")
		}
		return Result{Trust: "UNTRUSTED", State: "observed", Entries: raw}, nil
	case "backup_plan":
		return e.backupPlan(ctx, r)
	case "restore_plan":
		return e.restorePlan(ctx, r)
	}
	if r.Action != "backup_create" && r.Action != "backup_verify" {
		if _, err := Command(Request{Action: r.Action, Provider: r.Provider, Path: r.Path, Destination: r.Destination, ID: strings.Repeat("a", 32)}, "staging"); err != nil {
			return empty, err
		}
	}
	if r.Key == "" || len(r.Key) > 256 || r.Token != "" {
		return empty, ErrInvalid
	}
	// Owner and retry key select an immutable durable operation identity.
	sum := sha256.Sum256([]byte(r.Owner + "\x00" + r.Key))
	r.ID = hex.EncodeToString(sum[:16])
	raw, _ := json.Marshal(r)
	fp := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(fp[:])
	if old, err := e.load(r.ID, r.Owner); err == nil {
		if old.Fingerprint != fingerprint {
			return empty, errors.New("storage retry key conflict")
		}
		return old.Result, nil
	}
	if _, err := os.Lstat(e.opPath(r.ID)); !os.IsNotExist(err) {
		return empty, errors.New("storage operation requires reconciliation")
	}
	entries, err := os.ReadDir(e.Dir)
	if err != nil {
		return empty, err
	}
	if len(entries) > 1000 {
		return empty, errors.New("storage journal capacity reached; owner review required")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := privateFile(filepath.Join(e.Dir, entry.Name()))
		if err != nil {
			return empty, err
		}
		var active operation
		if json.Unmarshal(raw, &active) != nil {
			return empty, ErrInvalid
		}
		if r.Action == "backup_verify" && active.Result.ID == r.Destination && active.Request.Action == "backup_create" && active.Result.State == "UNKNOWN" && active.Result.SHA256 != "" {
			continue
		}
		if !e.settle(ctx, &active) {
			return empty, fmt.Errorf("storage is busy with operation %s (%s %s, %s): wait for it with storage_transfer_status id=%s", active.Result.ID, active.Request.Action, active.Request.Path, active.Result.State, active.Result.ID)
		}
	}
	if err := diskOK(e.Dir, 3*MaxFileBytes); err != nil {
		return empty, err
	}
	op := operation{Request: r, Fingerprint: fingerprint, Result: Result{Trust: "UNTRUSTED", ID: r.ID, State: "pending"}}
	if r.Action == "backup_create" || r.Action == "backup_verify" {
		if err := e.checkpoint(&op, "admitted", nil); err != nil {
			return empty, err
		}
	} else if err := e.save(op); err != nil {
		return empty, err
	}
	if _, err := command(ctx, "/usr/bin/systemd-run", UnitArgs(r.ID, true)...); err != nil {
		op.Result.State = "UNKNOWN"
		op.Result.Message = "launch outcome unknown; probe status"
		_ = e.save(op)
	}
	return op.Result, nil
}
func (e Engine) RunJob(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return ErrInvalid
	}
	raw, err := privateFile(e.opPath(id))
	if err != nil {
		return err
	}
	var op operation
	if json.Unmarshal(raw, &op) != nil || op.Result.ID != id || op.Result.State != "pending" {
		return ErrInvalid
	}
	if op.Result.Workflow != nil && op.Result.Workflow.Schema != BackupSchema {
		return errors.New("backup workflow schema incompatible: HOLD")
	}
	op.Result.State = "running"
	if err = e.save(op); err != nil {
		return err
	}
	switch op.Request.Action {
	case "backup_create":
		err = e.createBackup(ctx, &op)
	case "backup_verify":
		err = e.verifyBackup(ctx, &op)
	default:
		err = e.transfer(ctx, &op)
	}
	if err != nil {
		op.Result.State = "failed"
		if errors.Is(err, ErrUnknown) {
			op.Result.State = "UNKNOWN"
		}
		op.Result.Message = "operation failed; inspect source/destination before retry: " + reasonOf(err)
	} else {
		op.Result.State = "done"
	}
	return e.save(op)
}
func (e Engine) transfer(ctx context.Context, op *operation) error {
	r := op.Request
	local := ""
	if r.Action == "upload" {
		p := r.ArtifactPath
		inbox := filepath.Dir(p) == "/var/lib/mcpshka/artifact-inbox" && strings.HasSuffix(p, ".blob")
		if !inbox && !documentSource.MatchString(p) {
			return ErrInvalid
		}
		source, err := openUploadSource(p)
		if err != nil {
			return err
		}
		defer source.Close()
		local = filepath.Join(e.Dir, "staging", r.ID+".upload")
		if err = stageUpload(source, local, r.ArtifactSHA256, r.ArtifactSize); err != nil {
			return err
		}
		defer os.Remove(local)
	}
	if r.Action == "download" {
		// Under its own name (the extension tells the document type), in a
		// fresh folder only this operation id leads to.
		pruneDocuments(time.Now())
		dir := filepath.Join(DocumentsDir, r.ID)
		if err := os.Mkdir(dir, 0700); err != nil {
			return ErrInvalid
		}
		if err := os.Chmod(dir, 0755); err != nil {
			return err
		}
		local = filepath.Join(dir, path.Base(r.Path))
	}
	args, err := Command(r, local)
	if err != nil {
		return err
	}
	if _, err = e.rclone(ctx, r.Provider, args...); err != nil {
		return err
	}
	if r.Action == "download" {
		digest, size, err := hashFile(local)
		if err != nil {
			return err
		}
		if err = os.Chmod(local, 0644); err != nil {
			return err
		}
		op.Result.SHA256 = digest
		op.Result.Size = size
		op.Result.LocalPath = local
	}
	if r.Action == "trash" {
		op.Result.Message = "moved into MCPshka/.trash/" + r.ID + "; no provider purge"
	}
	return nil
}

// pruneDocuments drops downloads older than a week.
func pruneDocuments(now time.Time) {
	entries, _ := os.ReadDir(DocumentsDir)
	for _, d := range entries {
		if info, err := d.Info(); err == nil && idPattern.MatchString(d.Name()) && now.Sub(info.ModTime()) > 7*24*time.Hour {
			_ = os.RemoveAll(filepath.Join(DocumentsDir, d.Name()))
		}
	}
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxFileBytes+1))
	if err != nil || n > MaxFileBytes {
		return "", 0, errors.New("file budget exceeded")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func RandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}
