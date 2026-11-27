package rootbroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mcpshka/internal/approvedplan"
	"mcpshka/internal/dockersmoke"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// approved_task runs one pinned root command after the owner's Face ID on
// exactly that plan: executable (path + SHA-256), argv, workdir, writable
// paths, network and devices. It runs once, in a transient unit whose
// sandbox grants nothing beyond the plan; there is no shell and no general
// root. The approval page shows the generated command line, not agent text.

const (
	approvedScope = "approved_task"
	approvedOwner = "approved-task"
	approvedRoot  = taskRoot + "/approved"
)

var (
	approvedID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	approvedDenied = []string{"/proc", "/sys", "/boot", "/etc/sudoers", "/etc/shadow", "/etc/passwd", taskRoot}
)

type ApprovedPlan = approvedplan.Plan

type ApprovedResult struct {
	AutoExecute     bool         `json:"auto_execute,omitempty"`
	ID              string       `json:"id"`
	State           string       `json:"state"` // AWAITING_FACE_ID, RUNNING, DONE, FAILED
	Plan            ApprovedPlan `json:"plan"`
	PlanHash        string       `json:"plan_hash"`
	Approval        string       `json:"approval_url,omitempty"`
	ExitCode        *int         `json:"exit_code,omitempty"`
	Output          string       `json:"output,omitempty"`
	OutputTruncated bool         `json:"output_truncated,omitempty"`
	// OutputLive marks Output as the tail of a still running command
	// (MCP-115); the final output replaces it when the task ends.
	OutputLive bool       `json:"output_live,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	RenewedAt  *time.Time `json:"renewed_at,omitempty"`
	Renewals   int        `json:"renewals,omitempty"`
	// Effect of a terminal task: applied, none (nothing ran) or unknown.
	Effect string `json:"effect,omitempty"`
	// WorkerProtocol 1 workers record WorkerStartedAt before the plan's
	// command runs, so a vanished unit without it provably ran nothing.
	WorkerProtocol  int        `json:"worker_protocol,omitempty"`
	WorkerStartedAt *time.Time `json:"worker_started_at,omitempty"`
	// Computed on every read, never trusted from disk.
	ApprovalState    string     `json:"approval_state,omitempty"`
	ApprovalDeadline *time.Time `json:"approval_deadline,omitempty"`
	NextSafeAction   string     `json:"next_safe_action,omitempty"`
	// CoveredBy is the admin session that stood in for this plan's Face ID.
	CoveredBy string `json:"covered_by_session,omitempty"`
}

func (r ApprovedResult) terminal() bool {
	return r.State == "DONE" || r.State == "FAILED" || r.State == "EXPIRED"
}

func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\n")
}

// validateApproved checks the plan's shape; files are checked by
// inspectApproved, which can see /root (the broker runs with ProtectHome).
func validateApproved(p ApprovedPlan) error {
	if !cleanAbs(p.Executable) {
		return errors.New("executable must be an absolute clean path")
	}
	if len(p.Args) > 32 || p.TimeoutSec < 10 || p.TimeoutSec > 1800 {
		return errors.New("at most 32 args and timeout_sec 10..1800")
	}
	for _, a := range p.Args {
		if len(a) > 512 || strings.ContainsAny(a, "\x00\n") {
			return errors.New("args must be single-line and at most 512 bytes")
		}
	}
	if !cleanAbs(p.Workdir) {
		return errors.New("workdir must be an absolute clean path")
	}
	if len(p.Write) > 16 || len(p.Devices) > 8 || len(p.Inputs) > 8 {
		return errors.New("at most 16 write paths, 8 devices and 8 inputs")
	}
	for _, in := range p.Inputs {
		if !cleanAbs(in) {
			return errors.New("inputs must be absolute clean paths")
		}
	}
	for _, w := range p.Write {
		if !cleanAbs(w) || w == "/" {
			return errors.New("write paths must be absolute, clean and not /")
		}
		for _, d := range approvedDenied {
			if w == d || strings.HasPrefix(w, d+"/") || strings.HasPrefix(d, w+"/") {
				return fmt.Errorf("write path %s touches protected %s", w, d)
			}
		}
	}
	for _, d := range p.Devices {
		if !cleanAbs(d) || !strings.HasPrefix(d, "/dev/") {
			return errors.New("devices must be /dev paths")
		}
	}
	if err := p.ValidateDowngrade(); err != nil {
		return err
	}
	return validateApprovedChecks(p)
}

// pinnedChanged names the first changed executable/input and the first missing
// write path as the inspector sees them (the broker's /root is tmpfs: MCP-188).
func pinnedChanged(ctx context.Context, p ApprovedPlan) (changed, missing string) {
	got, err := runInspector(ctx, p)
	if err != nil {
		return "inspection failed: " + err.Error(), ""
	}
	if got.SHA256 != p.SHA256 {
		return p.Executable, ""
	}
	for _, in := range p.Inputs {
		if got.InputSHAs[in] != p.InputSHAs[in] {
			return in, ""
		}
	}
	return "", got.MissingWrite
}

type inspection struct {
	SHA256       string            `json:"sha256"`
	InputSHAs    map[string]string `json:"input_shas"`
	MissingWrite string            `json:"missing_write,omitempty"` // declared write path absent on the host
	Error        string            `json:"error,omitempty"`
}

// inspectApproved checks the plan's files and hashes them; it runs inside
// the inspector unit (ProtectHome=read-only), never in the broker itself.
func inspectApproved(p ApprovedPlan) inspection {
	fail := func(msg string) inspection { return inspection{Error: msg} }
	if validateRootOwnedExecutable(p.Executable) != nil {
		return fail("executable must be a root-owned, non-writable regular file: " + p.Executable)
	}
	if info, err := os.Stat(p.Workdir); err != nil || !info.IsDir() {
		return fail("workdir must be an existing directory: " + p.Workdir)
	}
	out := inspection{SHA256: fileSHA(p.Executable), InputSHAs: map[string]string{}}
	for _, in := range p.Inputs {
		info, err := os.Stat(in)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
			return fail("input must be an existing regular file up to 256 MiB: " + in)
		}
		if out.InputSHAs[in] = fileSHA(in); out.InputSHAs[in] == "" {
			return fail("input unreadable: " + in)
		}
	}
	if out.SHA256 == "" {
		return fail("executable unreadable")
	}
	out.MissingWrite = missingWritePath(p)
	return out
}

// RunApprovedTaskInspect is the inspector unit's entry point.
func RunApprovedTaskInspect(args []string) ([]byte, error) {
	if len(args) != 1 || filepath.Dir(args[0]) != approvedRoot {
		return nil, errors.New("inspector needs one plan file")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return nil, err
	}
	var p ApprovedPlan
	if json.Unmarshal(raw, &p) != nil {
		return nil, errors.New("invalid plan")
	}
	return json.Marshal(inspectApproved(p))
}

// runInspector checks the plan in a short read-only unit that sees /root.
func runInspector(ctx context.Context, p ApprovedPlan) (inspection, error) {
	if err := os.MkdirAll(approvedRoot, 0o700); err != nil {
		return inspection{}, err
	}
	f, err := os.CreateTemp(approvedRoot, "inspect-*.json")
	if err != nil {
		return inspection{}, err
	}
	defer os.Remove(f.Name())
	raw, _ := json.Marshal(p)
	f.Write(raw)
	f.Close()
	ictx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ictx, "/usr/bin/systemd-run", "--wait", "--pipe", "--quiet", "--collect",
		"--property=Type=exec", "--property=User=root", "--property=RuntimeMaxSec=60s",
		"--property=ProtectSystem=strict", "--property=ProtectHome=read-only", "--property=PrivateNetwork=yes",
		"--property=PrivateTmp=yes", "--property=NoNewPrivileges=yes", "--property=MemoryMax=128M", "--property=TasksMax=16",
		executorHelper, "approved-task-inspect", f.Name())
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	out := &limitedBuffer{limit: 64 << 10}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return inspection{}, fmt.Errorf("inspector: %w", err)
	}
	var got inspection
	if json.Unmarshal(out.Bytes(), &got) != nil {
		return inspection{}, errors.New("inspector output unreadable")
	}
	if got.Error != "" {
		return got, errors.New(got.Error)
	}
	return got, nil
}

func approvedHash(p ApprovedPlan) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// approvedPurpose is generated from the plan so the owner approves what will
// actually run.
func approvedPurpose(p ApprovedPlan, hash string) string {
	s := "root · сеть: " + map[bool]string{true: "да", false: "нет"}[p.Network]
	if p.AllowDowngrade {
		s += " · downgrade: " + p.TargetVersion
	}
	if len(p.Write) > 0 {
		s += " · запись: " + strings.Join(p.Write, ", ")
	}
	if len(p.Devices) > 0 {
		s += " · устройства: " + strings.Join(p.Devices, ", ")
	}
	for _, in := range p.Inputs {
		s += " · файл " + filepath.Base(in) + " " + p.InputSHAs[in][:12]
	}
	if len(p.Units) > 0 {
		s += " · службы: " + strings.Join(p.Units, ", ")
	}
	if len(p.Reach) > 0 {
		s += " · до запуска проверить: " + strings.Join(p.Reach, ", ")
	}
	if p.Probe != "" {
		s += " · проверка после: " + p.Probe
	}
	s += " · план " + hash[:12] + " · " + strings.Join(append([]string{p.Executable}, p.Args...), " ")
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

func approvedDir(id string) string { return filepath.Join(approvedRoot, id) }
func approvedUnit(id string) string {
	return "mcpshka-approved-" + id[:16] + ".service"
}

func loadApproved(id string) (ApprovedResult, error) { return loadApprovedAt(approvedRoot, id) }

func saveApproved(r ApprovedResult) error { return saveApprovedAt(approvedRoot, r) }

func runApprovedTask(ctx context.Context, args []string) ([]byte, error) {
	if len(args) == 1 && args[0] == "pending" {
		ids, err := pendingApproved(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(ids)
	}
	if len(args) == 1 && args[0] == "owned" {
		return json.Marshal(readOwnedUnits())
	}
	if len(args) < 2 || !approvedID.MatchString(args[1]) {
		return nil, errors.New("approved-task requires an action and a 32-hex id")
	}
	id := args[1]
	var r ApprovedResult
	var err error
	switch {
	case args[0] == "prepare" && len(args) == 3:
		r, err = prepareApproved(ctx, id, args[2])
	case args[0] == "execute" && len(args) == 2:
		r, err = executeApproved(ctx, id)
	case args[0] == "authorize-session" && len(args) == 4:
		r, err = authorizeApprovedSession(id, args[2], args[3], time.Now())
	case args[0] == "status" && len(args) == 2:
		r, err = approvedStatus(ctx, id)
	default:
		return nil, errors.New("unsupported approved-task action")
	}
	if err != nil {
		return nil, err
	}
	return approvedPublicJSON(r)
}

func approvedPublicJSON(r ApprovedResult) ([]byte, error) {
	clean := strings.ToValidUTF8(r.Output, "")
	r.OutputTruncated = r.OutputTruncated || clean != r.Output
	r.Output = clean
	if len(r.Output) > 2048 {
		r.Output = strings.ToValidUTF8(tail(r.Output, 2048), "")
		r.OutputTruncated = true
	}
	return json.Marshal(r)
}

func prepareApproved(ctx context.Context, id, planJSON string) (ApprovedResult, error) {
	var p ApprovedPlan
	if len(planJSON) > 16<<10 || json.Unmarshal([]byte(planJSON), &p) != nil {
		return ApprovedResult{}, errors.New("invalid plan JSON")
	}
	if err := validateApproved(p); err != nil {
		return ApprovedResult{}, err
	}
	got, err := runInspector(ctx, p)
	if err != nil {
		return ApprovedResult{}, err
	}
	if got.MissingWrite != "" {
		return ApprovedResult{}, errors.New(got.MissingWrite)
	}
	if err := p.CheckPins(got.SHA256, got.InputSHAs); err != nil {
		return ApprovedResult{}, err
	}
	p.SHA256, p.InputSHAs = got.SHA256, got.InputSHAs
	if old, err := loadApproved(id); err == nil {
		// Same id: idempotent replay of exactly the same pinned plan, which
		// also renews a lapsed approval. A different plan is never merged.
		if approvedHash(p) != old.PlanHash {
			return annotateApproved(old, time.Now()), errors.New("HOLD: this idempotency_key already names a different plan or the pinned files changed since prepare; review the change and prepare with a new idempotency_key")
		}
		r, err := renewApproved(old, time.Now().UTC())
		if err == nil {
			err = queueApprovedAt(approvedRoot, r)
		}
		return annotateApproved(r, time.Now()), err
	}
	if err := os.MkdirAll(approvedDir(id), 0o700); err != nil {
		return ApprovedResult{}, err
	}
	r := ApprovedResult{AutoExecute: true, ID: id, State: "AWAITING_FACE_ID", Plan: p, PlanHash: approvedHash(p), CreatedAt: time.Now().UTC()}
	pub, err := grantBegin(approvedScope, approvedOwner, id, r.PlanHash, "После Face ID сервер выполнит этот план один раз. "+approvedPurpose(p, r.PlanHash))
	if err != nil {
		return ApprovedResult{}, err
	}
	r.Approval = approvalLink(pub.Origin, id, time.Now())
	if err := saveApproved(r); err != nil {
		return r, err
	}
	return annotateApproved(r, time.Now()), queueApprovedAt(approvedRoot, r)
}

func executeApproved(ctx context.Context, id string) (ApprovedResult, error) {
	lock, err := openMutationLock(filepath.Join(approvedDir(id), "continuation.lock"))
	if err != nil {
		return ApprovedResult{}, err
	}
	defer lock.Close()
	r, err := loadApproved(id)
	if err != nil {
		return r, errors.New("approved task: unknown id")
	}
	if r.State != "AWAITING_FACE_ID" {
		return annotateApproved(r, time.Now()), nil
	}
	// The executable must still be the reviewed one.
	changed, missing := pinnedChanged(ctx, r.Plan)
	if changed != "" || approvedHash(r.Plan) != r.PlanHash {
		return r, errors.New("pinned file or plan changed since approval: HOLD " + changed)
	}
	// Checked before the grant is consumed, so the approval survives a fix.
	if missing != "" {
		return annotateApproved(r, time.Now()), errors.New("HOLD, nothing ran: " + missing)
	}
	if _, err := grantConsume(approvedScope, id, r.PlanHash); err != nil {
		r = annotateApproved(r, time.Now())
		return r, fmt.Errorf("%w (approval_state %s): %s", err, r.ApprovalState, r.NextSafeAction)
	}
	// Admit read-only status of the declared units from now on, whatever
	// the run's outcome: the owner approved this plan installing them.
	_ = recordOwnedUnits(r)
	now := time.Now().UTC()
	r.State, r.StartedAt, r.WorkerProtocol = "RUNNING", &now, 1
	if err := saveApproved(r); err != nil {
		return r, err
	}
	props := []string{"--unit=" + approvedUnit(id), "--collect", "--no-block", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=" + strconv.Itoa(r.Plan.TimeoutSec+15) + "s", "--property=TimeoutStopSec=10s",
		"--property=MemoryMax=512M", "--property=TasksMax=128", "--property=NoNewPrivileges=yes",
		"--property=PrivateTmp=yes", "--property=ProtectSystem=strict", "--property=ProtectHome=read-only",
		"--property=ReadWritePaths=" + strings.Join(append([]string{approvedDir(id)}, r.Plan.Write...), " ")}
	props = append(props, sessionSandbox(r)...)
	if !r.Plan.Network {
		props = append(props, "--property=PrivateNetwork=yes")
	}
	if len(r.Plan.Devices) == 0 {
		props = append(props, "--property=PrivateDevices=yes")
	} else {
		props = append(props, "--property=DevicePolicy=closed")
		for _, d := range r.Plan.Devices {
			props = append(props, "--property=DeviceAllow="+d+" rw")
		}
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", append(props, executorHelper, "approved-task-worker", id)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		end := time.Now().UTC()
		r.State, r.Error, r.FinishedAt = "FAILED", fmt.Sprintf("start worker: %v: %.300s", err, out), &end
		saveApproved(r)
		return r, nil
	}
	return r, nil
}

func approvedStatus(ctx context.Context, id string) (ApprovedResult, error) {
	r, err := loadApproved(id)
	if err != nil {
		return r, errors.New("approved task: unknown id")
	}
	if r.AutoExecute && r.State == "AWAITING_FACE_ID" {
		r, err = expireAutomaticApprovedAt(approvedRoot, id, time.Now().UTC())
		if err != nil {
			return r, err
		}
	}
	if r.State == "RUNNING" {
		// Only a state systemd reports counts as stopped: an empty answer
		// (systemctl itself failed) is not evidence the worker ended.
		alive := false
		switch dockersmoke.UnitState(ctx, approvedUnit(id)) {
		case "active", "activating", "reloading", "refreshing", "deactivating", "":
			alive = true
		}
		// Re-read: the worker may have finished between the two reads.
		if r2, e := loadApproved(id); e == nil && r2.terminal() {
			return annotateApproved(r2, time.Now()), nil
		}
		if !alive && r.StartedAt != nil && time.Since(*r.StartedAt) > dockersmoke.StartGrace {
			r = endedWithoutResult(r, time.Now().UTC())
		} else if live := readLiveTail(id); live != "" {
			r.Output, r.OutputLive = live, true
		}
	}
	return annotateApproved(r, time.Now()), nil
}

// RunApprovedTaskWorker runs inside the transient unit started above.
func RunApprovedTaskWorker(args []string) error {
	if os.Geteuid() != 0 || len(args) != 1 || !approvedID.MatchString(args[0]) {
		return errors.New("approved task worker requires root and an id")
	}
	r, err := loadApproved(args[0])
	if err != nil || r.State != "RUNNING" {
		return errors.New("approved task is not running")
	}
	finish := func(state string, code *int, out, msg string) error {
		end := time.Now().UTC()
		r.State, r.ExitCode, r.Output, r.Error, r.FinishedAt = state, code, tail(out, 8192), msg, &end
		return saveApproved(r)
	}
	// The worker unit sees /root itself: check the pins directly.
	if got := inspectApproved(r.Plan); got.Error != "" || got.SHA256 != r.Plan.SHA256 || !maps.Equal(got.InputSHAs, r.Plan.InputSHAs) {
		return finish("FAILED", nil, "", "pinned file changed after approval "+got.Error)
	}
	// Recorded before the command: without this mark status proves effect none.
	started := time.Now().UTC()
	r.WorkerStartedAt = &started
	if err := saveApproved(r); err != nil {
		return fmt.Errorf("record worker start, nothing ran: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.Plan.TimeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Plan.Executable, r.Plan.Args...)
	cmd.Dir, cmd.Env = r.Plan.Workdir, r.Plan.Environment()
	out := &limitedBuffer{limit: 64 << 10}
	var w io.Writer = out
	// The same bytes go to a bounded live file so status shows progress
	// while the command runs (MCP-115); it is the task's own directory.
	if f, ferr := os.OpenFile(liveLogPath(r.ID), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600); ferr == nil {
		defer f.Close()
		w = io.MultiWriter(out, &cappedFile{f: f, left: liveLogLimit})
	}
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	state := "DONE"
	msg := ""
	if err != nil {
		state, msg = "FAILED", tail(err.Error(), 300)
		// A bare «exit status 1» told nothing (MCP-177): the command ran and
		// stayed silent, which for a search is «no matches», not a refusal.
		if code == 1 && len(bytes.TrimSpace(out.Bytes())) == 0 {
			msg += ": the command ran and printed nothing. For a search (grep, journalctl -g, diff, test) exit 1 means NO_MATCHES, not an access error; access errors print a message"
		}
	}
	return finish(state, &code, string(out.Bytes()), msg)
}

// missingWritePath names the first declared write path that does not exist.
// systemd cannot bind a missing ReadWritePaths entry and fails at NAMESPACE
// before the worker runs; a directory the plan creates needs its parent.
func missingWritePath(p ApprovedPlan) string {
	for _, w := range p.Write {
		if _, err := os.Lstat(w); err != nil {
			return "write path " + w + " does not exist; declare its existing parent " + filepath.Dir(w) + " to create it"
		}
	}
	return ""
}

// endedWithoutResult classifies a RUNNING task whose unit is gone.
func endedWithoutResult(r ApprovedResult, end time.Time) ApprovedResult {
	r.State, r.Error, r.FinishedAt = "FAILED", "worker ended without a result (killed or timed out)", &end
	if r.WorkerProtocol >= 1 && r.WorkerStartedAt == nil {
		// e3af7683 (01.11): systemd failed at NAMESPACE (status 226) before
		// the worker ran; the plan's command never started.
		r.Error, r.Effect = "worker never started: unit "+approvedUnit(r.ID)+" failed before exec (see its journal)", "none"
	}
	return r
}

const liveLogLimit = 64 << 10

func liveLogPath(id string) string { return filepath.Join(approvedDir(id), "live.log") }

// cappedFile stops writing after left bytes but never fails the command.
type cappedFile struct {
	f    *os.File
	left int
}

func (c *cappedFile) Write(p []byte) (int, error) {
	if c.left > 0 {
		n := len(p)
		if n > c.left {
			n = c.left
		}
		c.f.Write(p[:n])
		c.left -= n
	}
	return len(p), nil
}

// readLiveTail returns the last 2 KiB of a running task's output.
func readLiveTail(id string) string { return fileTail(liveLogPath(id), 2048) }

func fileTail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, e := f.Stat(); e == nil && st.Size() > n {
		f.Seek(st.Size()-n, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, n))
	return strings.ToValidUTF8(string(b), "")
}
