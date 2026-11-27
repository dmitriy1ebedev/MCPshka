package rootbroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/releaseapplystatus"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	taskRoot      = "/var/lib/mcpshka-executor"
	haAutomations = "/opt/homeassistant/config/automations.yaml"
	rootTaskTTL   = 60 * time.Minute
)

type rootTaskPermit struct {
	AdminSessionID  string    `json:"admin_session_id,omitempty"`
	AdminGeneration int64     `json:"admin_generation,omitempty"`
	AuthMethod      string    `json:"auth_method,omitempty"`
	AuthorizedAt    string    `json:"authorized_at,omitempty"`
	PlanVersion     int       `json:"plan_version,omitempty"`
	ID              string    `json:"id"`
	Owner           string    `json:"owner"`
	Scope           string    `json:"scope"`
	LiveSHA         string    `json:"live_sha"`
	StagedSHA       string    `json:"staged_sha"`
	State           string    `json:"state"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type rootTaskResult struct {
	AdminSessionID     string    `json:"admin_session_id,omitempty"`
	AdminGeneration    int64     `json:"admin_generation,omitempty"`
	AuthorizationState string    `json:"authorization_state,omitempty"`
	AuthMethod         string    `json:"auth_method,omitempty"`
	AuthorizedAt       string    `json:"authorized_at,omitempty"`
	PlanVersion        int       `json:"plan_version,omitempty"`
	TaskID             string    `json:"task_id"`
	Scope              string    `json:"scope"`
	State              string    `json:"state"`
	ExpiresAt          time.Time `json:"expires_at,omitempty"`
	LiveSHA            string    `json:"live_sha,omitempty"`
	StagedSHA          string    `json:"staged_sha,omitempty"`
	BackupCreated      bool      `json:"backup_created,omitempty"`
	ReloadRequired     bool      `json:"reload_required,omitempty"`
}

func taskPath(id string) string   { return filepath.Join(taskRoot, "task-"+id+".json") }
func taskBackup(id string) string { return filepath.Join(taskRoot, "task-"+id+"-automations.backup") }

func readFixedRegular(path string) ([]byte, os.FileInfo, error) {
	return releaseapplystatus.ReadProtected(path)
}
func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func syncRootParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func safeOwner(owner string) bool {
	if len(owner) < 1 || len(owner) > 160 {
		return false
	}
	for _, r := range owner {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func permitResult(p rootTaskPermit) rootTaskResult {
	return rootTaskResult{AdminSessionID: p.AdminSessionID, AdminGeneration: p.AdminGeneration, AuthMethod: p.AuthMethod, AuthorizedAt: p.AuthorizedAt, PlanVersion: p.PlanVersion, TaskID: p.ID, Scope: p.Scope, State: p.State, ExpiresAt: p.ExpiresAt, LiveSHA: p.LiveSHA, StagedSHA: p.StagedSHA}
}
func openPermit(id string, create bool) (*os.File, error) {
	if !validHex(id, 32) {
		return nil, errors.New("invalid task identifier")
	}
	if create {
		fd, e := syscall.Open(taskPath(id), syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
		if e != nil {
			return nil, e
		}
		f := os.NewFile(uintptr(fd), taskPath(id))
		return f, nil
	}
	// Status probes before authorization must not create an empty permit.
	// A missing record is reported as not_found by readRootTask.
	fd, err := syscall.Open(taskPath(id), syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), taskPath(id))
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Nlink != 1 || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		f.Close()
		return nil, errors.New("unsafe task permission record")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func loadPermit(f *os.File) (rootTaskPermit, error) {
	var p rootTaskPermit
	st, e := f.Stat()
	if e != nil {
		return p, e
	}
	if st.Size() < 2 || st.Size() > 4096 {
		return p, errors.New("invalid task record")
	}
	raw := make([]byte, st.Size())
	if _, e = f.ReadAt(raw, 0); e != nil {
		return p, e
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return p, errors.New("invalid task record")
	}
	if !validHex(p.ID, 32) || !safeOwner(p.Owner) || p.Scope != "homeassistant_automations" || !validHash(p.LiveSHA) || !validHash(p.StagedSHA) {
		return p, errors.New("invalid task data")
	}
	if p.PlanVersion == rootTaskPlanVersion {
		if p.AuthMethod != "totp" && p.AuthMethod != "webauthn" {
			return p, errors.New("invalid authorization receipt method")
		}
		if _, err := time.Parse(time.RFC3339Nano, p.AuthorizedAt); err != nil {
			return p, errors.New("invalid authorization receipt timestamp")
		}
	}
	if p.AdminSessionID != "" || p.AdminGeneration != 0 {
		if !validHex(p.AdminSessionID, 32) || p.AdminGeneration <= 0 || p.AuthMethod != "webauthn" || p.PlanVersion != rootTaskPlanVersion {
			return p, errors.New("invalid administrative task receipt")
		}
	}
	return p, nil
}
func validateTaskAccess(p rootTaskPermit, owner string, now time.Time) error {
	if !safeOwner(owner) || p.Owner != owner {
		return errors.New("task owner not authorized")
	}
	if p.AdminSessionID != "" {
		if !validHex(p.AdminSessionID, 32) || p.AdminGeneration <= 0 || p.AuthMethod != "webauthn" || p.PlanVersion != rootTaskPlanVersion {
			return errors.New("invalid administrative task binding")
		}
	} else if p.AdminGeneration != 0 || !now.Before(p.ExpiresAt) {
		return errors.New("root task expired")
	}
	if p.State == "revoked" {
		return errors.New("root task revoked")
	}
	return nil
}
func authorizeRootTask(args []string, now time.Time) (rootTaskResult, error) {
	if len(args) != 7 {
		return rootTaskResult{}, errors.New("authorization requires id/scope/file hashes/owner/TOTP code")
	}
	id, scope, liveHash, stagedHash, owner, code := args[1], args[2], args[3], args[4], args[5], args[6]
	if e := validateTaskFields(id, scope, liveHash, stagedHash); e != nil {
		return rootTaskResult{}, e
	}
	if !safeOwner(owner) {
		return rootTaskResult{}, errors.New("invalid owner identity")
	}
	lock, e := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if e != nil {
		return rootTaskResult{}, e
	}
	defer lock.Close()
	live, _, e := readFixedRegular(haAutomations)
	if e != nil {
		return rootTaskResult{}, e
	}
	if shaHex(live) != liveHash {
		return rootTaskResult{}, errors.New("production SHA changed since review")
	}
	if liveHash == stagedHash {
		return rootTaskResult{}, errors.New("staged content must differ from production")
	}
	if e = consumeTOTP(code, now, totpSeedFile, totpLedgerFile); e != nil {
		return rootTaskResult{}, e
	}
	p := rootTaskPermit{AuthMethod: "totp", AuthorizedAt: now.UTC().Format(time.RFC3339Nano), PlanVersion: rootTaskPlanVersion, ID: id, Scope: scope, Owner: owner, LiveSHA: liveHash, StagedSHA: stagedHash, State: "authorized", ExpiresAt: now.Add(rootTaskTTL)}
	f, e := openPermit(id, true)
	if e != nil {
		return rootTaskResult{}, e
	}
	defer f.Close()
	if e = saveJSONState(f, p); e != nil {
		return rootTaskResult{}, e
	}
	if e = saveRootApproval(p); e != nil {
		return rootTaskResult{}, e
	}
	return permitResult(p), nil
}
func readRootTask(args []string, now time.Time) (rootTaskResult, error) {
	if len(args) != 3 || !validHex(args[1], 32) {
		return rootTaskResult{}, errors.New("task status requires id and owner")
	}
	f, e := openPermit(args[1], false)
	if os.IsNotExist(e) {
		return rootTaskResult{TaskID: args[1], State: "not_found"}, nil
	}
	if e != nil {
		return rootTaskResult{}, e
	}
	defer f.Close()
	p, e := loadPermit(f)
	if e != nil {
		return rootTaskResult{}, e
	}
	if !safeOwner(args[2]) || p.Owner != args[2] {
		return rootTaskResult{}, errors.New("task owner not authorized")
	}
	result := permitResult(p)
	if p.AdminSessionID != "" {
		current, err := readAdminSession(taskRoot, now)
		if err != nil {
			result.AuthorizationState = "unknown"
		} else if requireAdminSession(current, p.AdminSessionID, p.Owner, p.AdminGeneration, now) != nil {
			result.AuthorizationState = "blocked"
		} else {
			result.AuthorizationState = "active"
		}
	} else if !now.Before(p.ExpiresAt) {
		result.State = "expired"
	}
	if result.State == "authorized" && requireRootApproval(p) != nil {
		result.State = "blocked"
	}
	return result, nil
}
func validateHAContent(ctx context.Context, data []byte) error {
	if len(data) < 10 || len(data) > 49152 {
		return errors.New("automation YAML length out of bounds")
	}
	py := `import sys,yaml
obj=yaml.safe_load(sys.stdin.read())
if not isinstance(obj,list) or not obj or len(obj)>1000: raise ValueError("automation list required")
ids=[x.get("id") for x in obj if isinstance(x,dict)]
if len(ids)!=len(obj) or not all(isinstance(x,str) and x for x in ids) or len(ids)!=len(set(ids)): raise ValueError("unique automation IDs required")
`
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/python3", "-I", "-c", py)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "PYTHONNOUSERSITE=1"}
	cmd.Stdin = bytes.NewReader(data)
	if e := cmd.Run(); e != nil {
		return errors.New("staged automation YAML validation failed")
	}
	return nil
}

// Both apply and rollback hold the same resource lock, even across task IDs.
func openMutationLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("unsafe mutation lock")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("configuration mutation busy: HOLD")
	}
	return f, nil
}

func decodeTaskContent(b64parts []string, stagedSHA string) ([]byte, error) {
	if len(b64parts) < 1 || len(b64parts) > 20 {
		return nil, errors.New("invalid content chunks")
	}
	var base strings.Builder
	for _, part := range b64parts {
		if len(part) < 1 || len(part) > 3000 {
			return nil, errors.New("invalid content chunk")
		}
		base.WriteString(part)
	}
	if base.Len() > 65536 {
		return nil, errors.New("automation payload too large")
	}
	data, err := base64.StdEncoding.DecodeString(base.String())
	if err != nil {
		return nil, errors.New("invalid staging encoding")
	}
	if shaHex(data) != stagedSHA {
		return nil, errors.New("staging SHA mismatched approved plan")
	}
	return data, nil
}

func applyHA(ctx context.Context, p rootTaskPermit, b64parts []string) error {
	lock, err := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := requireRootApproval(p); err != nil {
		return err
	}
	data, e := decodeTaskContent(b64parts, p.StagedSHA)
	if e != nil {
		return e
	}
	if e = validateHAContent(ctx, data); e != nil {
		return e
	}
	current, meta, e := readFixedRegular(haAutomations)
	if e != nil {
		return e
	}
	if shaHex(current) != p.LiveSHA {
		return errors.New("production SHA changed: HOLD")
	}
	backup, e := syscall.Open(taskBackup(p.ID), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return fmt.Errorf("create root-owned backup: %w", e)
	}
	bf := os.NewFile(uintptr(backup), taskBackup(p.ID))
	if _, e = bf.Write(current); e == nil {
		e = bf.Sync()
	}
	closeErr := bf.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = syncRootParent(taskBackup(p.ID)); e != nil {
		return e
	}
	// Recheck after backup to refuse a concurrent, independently changed config.
	fresh, _, e := readFixedRegular(haAutomations)
	if e != nil {
		return e
	}
	if shaHex(fresh) != p.LiveSHA {
		return errors.New("production changed during backup: HOLD")
	}
	f, e := os.CreateTemp(filepath.Dir(haAutomations), ".mcpshka-ha-*.yaml")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(meta.Mode().Perm()); e != nil {
		f.Close()
		return e
	}
	if st, ok := meta.Sys().(*syscall.Stat_t); ok {
		if e = f.Chown(int(st.Uid), int(st.Gid)); e != nil {
			f.Close()
			return e
		}
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	fresh, _, e = readFixedRegular(haAutomations)
	if e != nil || shaHex(fresh) != p.LiveSHA {
		return errors.New("production changed before apply: HOLD")
	}
	if e = os.Rename(tmp, haAutomations); e != nil {
		return e
	}
	return syncRootParent(haAutomations)
}
func rollbackHA(p rootTaskPermit) error {
	lock, err := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := requireRootApproval(p); err != nil {
		return err
	}
	current, meta, err := readFixedRegular(haAutomations)
	if err != nil {
		return err
	}
	if shaHex(current) != p.StagedSHA {
		return errors.New("production differs from the applied task: HOLD")
	}
	backup, _, err := readFixedRegular(taskBackup(p.ID))
	if err != nil {
		return err
	}
	if shaHex(backup) != p.LiveSHA {
		return errors.New("task backup checksum differs from original: HOLD")
	}
	f, err := os.CreateTemp(filepath.Dir(haAutomations), ".mcpshka-ha-rollback-*.yaml")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(meta.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	if st, ok := meta.Sys().(*syscall.Stat_t); ok {
		if err = f.Chown(int(st.Uid), int(st.Gid)); err != nil {
			f.Close()
			return err
		}
	}
	if _, err = f.Write(backup); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	fresh, _, err := readFixedRegular(haAutomations)
	if err != nil {
		return err
	}
	if shaHex(fresh) != p.StagedSHA {
		return errors.New("production changed during rollback: HOLD")
	}
	if err = os.Rename(tmp, haAutomations); err != nil {
		return err
	}
	return syncRootParent(haAutomations)
}

func executeRootTask(ctx context.Context, args []string, now time.Time) (rootTaskResult, error) {
	if len(args) < 4 {
		return rootTaskResult{}, errors.New("task action requires action/id/owner")
	}
	action, id, owner := args[1], args[2], args[3]
	if !validHex(id, 32) || !safeOwner(owner) {
		return rootTaskResult{}, errors.New("invalid task identity")
	}
	sessionLock, e := openAdminSessionLock(taskRoot)
	if e != nil {
		return rootTaskResult{}, e
	}
	defer sessionLock.Close()
	f, e := openPermit(id, false)
	if e != nil {
		return rootTaskResult{}, e
	}
	defer f.Close()
	p, e := loadPermit(f)
	if e != nil {
		return rootTaskResult{}, e
	}
	if e = validateTaskAccess(p, owner, now); e != nil {
		return rootTaskResult{}, e
	}
	if p.PlanVersion != rootTaskPlanVersion {
		return rootTaskResult{}, errors.New("unsupported approval plan version; prepare a new task")
	}
	if p.AdminSessionID != "" {
		current, err := readAdminSession(taskRoot, now)
		if err != nil {
			return rootTaskResult{}, err
		}
		if err := requireAdminSession(current, p.AdminSessionID, owner, p.AdminGeneration, now); err != nil {
			return rootTaskResult{}, err
		}
	}
	switch action {
	case "apply":
		if p.State == "applied" {
			current, _, err := readFixedRegular(haAutomations)
			if err != nil || shaHex(current) != p.StagedSHA {
				return rootTaskResult{}, errors.New("applied automation state changed: HOLD")
			}
			out := permitResult(p)
			out.BackupCreated = true
			out.ReloadRequired = true
			return out, nil
		}
		if p.State != "authorized" {
			return rootTaskResult{}, errors.New("task cannot apply in current state")
		}
		if e = applyHA(ctx, p, args[4:]); e != nil {
			return rootTaskResult{}, e
		}
		p.State = "applied"
	case "rollback":
		// Reconcile a crash after atomic replace but before saving "applied".
		// The pinned backup+production hashes decide, not the stale receipt.
		if len(args) != 4 || (p.State != "applied" && p.State != "authorized") {
			return rootTaskResult{}, errors.New("only a previously applied task can rollback")
		}
		if e = rollbackHA(p); e != nil {
			return rootTaskResult{}, e
		}
		p.State = "rolled_back"
	case "close":
		if len(args) != 4 {
			return rootTaskResult{}, errors.New("unexpected close arguments")
		}
		p.State = "revoked"
	default:
		return rootTaskResult{}, errors.New("unsupported root task action")
	}
	if e = saveJSONState(f, p); e != nil {
		return rootTaskResult{}, fmt.Errorf("action performed but task receipt could not be saved: %w", e)
	}
	out := permitResult(p)
	if action == "apply" || action == "rollback" {
		out.BackupCreated = true
		out.ReloadRequired = true
	}
	return out, nil
}
func RunRootTask(ctx context.Context, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("root task operation required")
	}
	var out rootTaskResult
	var err error
	switch args[0] {
	case "inspect":
		if len(args) != 1 {
			return nil, errors.New("unexpected root task inspect arguments")
		}
		var live []byte
		live, _, err = readFixedRegular(haAutomations)
		if err == nil {
			out = rootTaskResult{PlanVersion: rootTaskPlanVersion, Scope: "homeassistant_automations", State: "available", LiveSHA: shaHex(live)}
		}
	case "authorize":
		out, err = authorizeRootTask(args, time.Now())
	case "status":
		out, err = readRootTask(args, time.Now())
	case "execute":
		out, err = executeRootTask(ctx, args, time.Now())
	default:
		return nil, errors.New("unsupported root task operation")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
