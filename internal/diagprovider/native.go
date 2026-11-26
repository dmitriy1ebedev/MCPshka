package diagprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Investigation struct {
	Path             string `json:"path" jsonschema:"DIAG repository inside the allowed roots, normally /root/repos/diag"`
	Target           string `json:"target" jsonschema:"host or self"`
	Reason           string `json:"reason" jsonschema:"why, one line, 1..512 chars"`
	Risk             string `json:"risk" jsonschema:"must be read_only"`
	TimeoutSeconds   int64  `json:"timeout_seconds" jsonschema:"1..30"`
	MaxBytes         int    `json:"max_bytes" jsonschema:"2048..16384"`
	MemoryLimitBytes int64  `json:"memory_limit_bytes" jsonschema:"67108864..268435456 (64..256 MiB)"`
	CPUTimeSeconds   int64  `json:"cpu_time_seconds" jsonschema:"1..5 and not above timeout_seconds"`
}
type Native struct {
	manager *jobs.Manager
	binary  string
}
type NativeHealth struct {
	Schema            string `json:"schema"`
	Version           string `json:"version"`
	Ready             bool   `json:"ready"`
	RateQuotaEnforced bool   `json:"rate_quota_enforced"`
	Trust             string `json:"trust"`
}
type NativeRun struct {
	ID                string   `json:"id"`
	State             string   `json:"state"`
	ResourcesReleased bool     `json:"resources_released"`
	Evidence          Evidence `json:"evidence"`
	Report            string   `json:"report,omitempty"`
}
type nativeIntent struct {
	ID          string
	Input       Investigation
	Fingerprint string
	Version     string
	Supervisor  jobs.OwnerIdentity
	StartedAt   time.Time
}

func NewNative(m *jobs.Manager) *Native { return &Native{manager: m, binary: "/usr/local/bin/diag"} }

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.overflow = true
		return 0, ErrUnsafe
	}
	return b.Buffer.Write(p)
}
func (n *Native) read(ctx context.Context, args ...string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, n.binary, args...)
	cmd.Env = policy.MinimalEnv("")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out := &boundedBuffer{limit: 65536}
	cmd.Stdout = out
	cmd.Stderr = &boundedBuffer{limit: 2048}
	e := cmd.Run()
	if out.overflow {
		return nil, ErrUnsafe
	}
	return out.Bytes(), e
}
func (n *Native) Health(ctx context.Context) (NativeHealth, error) {
	out := NativeHealth{Schema: DIAGCapabilitiesSchema, Trust: "UNTRUSTED", RateQuotaEnforced: n.manager.SystemdScopesEnabled()}
	raw, e := n.read(ctx, "capabilities", "--json")
	if e != nil {
		return out, e
	}
	if _, e = ParseDIAGCapabilities(raw); e != nil {
		return out, e
	}
	raw, e = n.read(ctx, "version")
	if e != nil {
		return out, e
	}
	line := strings.SplitN(string(raw), "\n", 2)[0]
	parts := strings.Fields(line)
	if len(parts) != 2 || parts[0] != "diag" {
		return out, ErrIncompatible
	}
	version := parts[1]
	if version == "" || len(version) > 128 || strings.ContainsAny(version, "\r\n\x00") {
		return out, ErrIncompatible
	}
	out.Version = version
	out.Ready = true
	return out, nil
}
func nativeKey(id string) (string, error) {
	if len(id) != 41 || !strings.HasPrefix(id, "workflow-") {
		return "", ErrUnsafe
	}
	if _, e := hex.DecodeString(id[9:]); e != nil {
		return "", ErrUnsafe
	}
	return "diag/v1/runs/" + id[9:], nil
}
func (n *Native) intent(id string) (nativeIntent, error) {
	var out nativeIntent
	k, e := nativeKey(id)
	if e != nil {
		return out, e
	}
	if n.manager.Database() == nil {
		return out, ErrIncompatible
	}
	raw, e := n.manager.Database().Get(k)
	if e != nil {
		return out, e
	}
	if len(raw) > 4096 {
		return out, ErrUnsafe
	}
	e = json.Unmarshal(raw, &out)
	if e != nil || out.ID != id {
		return out, ErrUnsafe
	}
	if validateInvestigation(out.Input) != nil {
		return out, ErrUnsafe
	}
	root, checkErr := n.manager.Policy().CheckWorkdir(out.Input.Path)
	if checkErr != nil || root != out.Input.Path {
		return out, ErrUnsafe
	}
	raw, _ = json.Marshal(out.Input)
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != out.Fingerprint {
		return out, ErrUnsafe
	}
	return out, nil
}
func validateInvestigation(in Investigation) error {
	bad := func(field string) error { return fmt.Errorf("%w: %s", ErrUnsafe, field) }
	switch {
	case in.Target != "host" && in.Target != "self":
		return bad("target must be host or self")
	case in.Risk != "read_only":
		return bad("risk must be read_only")
	case in.Reason == "" || len(in.Reason) > 512 || strings.ContainsAny(in.Reason, "\r\n\x00"):
		return bad("reason must be one line of 1..512 chars")
	case in.TimeoutSeconds < 1 || in.TimeoutSeconds > 30:
		return bad("timeout_seconds must be 1..30")
	case in.MaxBytes < 2048 || in.MaxBytes > 16384:
		return bad("max_bytes must be 2048..16384")
	case in.MemoryLimitBytes < 64<<20 || in.MemoryLimitBytes > 256<<20:
		return bad("memory_limit_bytes must be 64..256 MiB")
	case in.CPUTimeSeconds < 1 || in.CPUTimeSeconds > 5 || in.CPUTimeSeconds > in.TimeoutSeconds:
		return bad("cpu_time_seconds must be 1..5 and not above timeout_seconds")
	}
	return nil
}
func (n *Native) Start(ctx context.Context, key string, in Investigation) (NativeRun, error) {
	if n.manager.Database() == nil {
		return NativeRun{}, ErrIncompatible
	}
	if key == "" || len(key) > 128 {
		return NativeRun{}, ErrUnsafe
	}
	if e := validateInvestigation(in); e != nil {
		return NativeRun{}, e
	}
	root, e := n.manager.Policy().CheckWorkdir(in.Path)
	if e != nil {
		return NativeRun{}, e
	}
	in.Path = root
	if _, e := n.manager.Policy().DecideCommandForWorkdir("mcpshka-diag-start", nil, root); e != nil {
		return NativeRun{}, e
	}
	sum := sha256.Sum256([]byte("diag-native-v1:" + key))
	id := "workflow-" + hex.EncodeToString(sum[:16])
	ledger, _ := nativeKey(id)
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(hash[:])
	if old, e := n.intent(id); e == nil {
		if old.Fingerprint != fingerprint {
			return NativeRun{}, ErrUnsafe
		}
		return n.Status(ctx, id)
	} else if !os.IsNotExist(e) {
		return NativeRun{}, e
	}
	health, e := n.Health(ctx)
	if e != nil {
		return NativeRun{}, e
	}
	keys, e := n.manager.Database().List("diag/v1/runs/")
	if e != nil {
		return NativeRun{}, e
	}
	if len(keys) >= 512 {
		return NativeRun{}, ErrUnsafe
	}
	owner, e := jobs.CurrentOwnerIdentity()
	if e != nil {
		return NativeRun{}, e
	}
	intent := nativeIntent{ID: id, Input: in, Fingerprint: fingerprint, Version: health.Version, Supervisor: owner, StartedAt: time.Now().UTC()}
	raw, _ = json.Marshal(intent)
	inserted, e := n.manager.Database().PutIfAbsent(ledger, raw)
	if e != nil {
		return NativeRun{}, e
	}
	if !inserted {
		old, e := n.intent(id)
		if e != nil {
			return NativeRun{}, e
		}
		if old.Fingerprint != fingerprint {
			return NativeRun{}, ErrUnsafe
		}
		return n.Status(ctx, id)
	}
	// Durable identity precedes dispatch. Missing launch receipt remains UNKNOWN,
	// never a reason to start again, including after an authority restart.
	_, e = n.manager.StartWithOptions(n.binary, []string{"agent", "--json", "--scope", in.Target, "--budget-bytes", strconv.Itoa(in.MaxBytes)}, root, jobs.StartOptions{ReservedID: id, Timeout: time.Duration(in.TimeoutSeconds) * time.Second, OutputLimitBytes: int64(in.MaxBytes), MemoryLimitBytes: in.MemoryLimitBytes, CPUTimeLimit: time.Duration(in.CPUTimeSeconds) * time.Second, CPUQuotaPercent: 50, ProcessLimit: 16})
	if e != nil {
		return NativeRun{ID: id, State: "unknown"}, fmt.Errorf("%w: native launch not accepted", ErrUnknown)
	}
	return n.Status(ctx, id)
}
func (n *Native) boundJob(in nativeIntent) (jobs.Snapshot, error) {
	j, e := n.manager.Get(in.ID)
	if e != nil {
		return j, e
	}
	args := []string{"agent", "--json", "--scope", in.Input.Target, "--budget-bytes", strconv.Itoa(in.Input.MaxBytes)}
	a, _ := json.Marshal(args)
	b, _ := json.Marshal(j.Args)
	if j.Command != n.binary || j.Workdir != in.Input.Path || !bytes.Equal(a, b) {
		return jobs.Snapshot{}, ErrUnsafe
	}
	return j, nil
}
func (n *Native) Status(ctx context.Context, id string) (NativeRun, error) {
	in, e := n.intent(id)
	if e != nil {
		return NativeRun{}, e
	}
	out := NativeRun{ID: id, State: "unknown"}
	j, e := n.boundJob(in)
	if e != nil {
		if errors.Is(e, ErrUnsafe) {
			return NativeRun{}, e
		}
		// Proof only releases resources. It never fabricates success or authorizes replay.
		e = n.manager.WithMissingReceipt(in.Input.Path, id, in.Supervisor, false, func(*jobs.Snapshot) error { out.ResourcesReleased = true; return nil })
		return out, nil
	}
	if j.State == jobs.Unknown {
		if got, e := n.manager.Reconcile(id); e == nil {
			j = got
		}
	}
	out.State = string(j.State)
	out.ResourcesReleased = j.ResourcesReleased
	if j.State != jobs.Done || j.ExitCode == nil || *j.ExitCode != 0 {
		return out, nil
	}
	if e = n.manager.WithGoneOwner(in.Input.Path, id, func(jobs.Snapshot) error { return nil }); e != nil {
		out.State = "reconciling"
		return out, nil
	}
	out.ResourcesReleased = true
	if j.OutputDroppedBytes != 0 {
		return NativeRun{}, ErrUnsafe
	}
	data, e := n.manager.Read(id, "stdout", 0, in.Input.MaxBytes)
	if e != nil {
		return NativeRun{}, e
	}
	if data.Truncated || data.TotalBytes > int64(in.Input.MaxBytes) {
		return NativeRun{}, ErrUnsafe
	}
	evidence, e := parseAgent([]byte(data.Text), id, in.Version, in.Input.Target, in.Input.MaxBytes, in.StartedAt)
	if e != nil {
		return NativeRun{}, e
	}
	out.Report = string(evidence.Data)
	evidence.Data = nil
	out.Evidence = evidence
	return out, nil
}
func (n *Native) Cancel(ctx context.Context, id string) (NativeRun, error) {
	in, e := n.intent(id)
	if e != nil {
		return NativeRun{}, e
	}
	j, e := n.boundJob(in)
	if e != nil {
		return NativeRun{}, e
	}
	if j.State == jobs.Unknown {
		return NativeRun{}, ErrUnknown
	}
	if _, e = n.manager.Cancel(id); e != nil {
		return NativeRun{}, e
	}
	return n.Status(ctx, id)
}
func parseAgent(data []byte, id, version, target string, maxBytes int, started time.Time) (Evidence, error) {
	if len(data) == 0 || len(data) > maxBytes {
		return Evidence{}, ErrUnsafe
	}
	var wire struct {
		Schema     string    `json:"schema_version"`
		CapturedAt time.Time `json:"generated_at"`
		Version    string    `json:"diag_version"`
		Redaction  struct {
			Applied bool   `json:"applied"`
			Policy  string `json:"policy"`
		} `json:"redaction"`
		Scope struct {
			Effective string `json:"effective"`
			Satisfied bool   `json:"satisfied"`
		} `json:"scope"`
	}
	if e := json.Unmarshal(data, &wire); e != nil {
		return Evidence{}, ErrIncompatible
	}
	if wire.Schema != "diag.agent/v1alpha1" || wire.Version != version || !wire.Redaction.Applied || wire.Redaction.Policy != "ai-safe/v1" {
		return Evidence{}, ErrIncompatible
	}
	if !wire.Scope.Satisfied || wire.Scope.Effective != target || wire.CapturedAt.IsZero() || wire.CapturedAt.Before(started.Add(-time.Second)) || wire.CapturedAt.After(time.Now().Add(time.Minute)) {
		return Evidence{}, ErrUnsafe
	}
	return Evidence{Source: "DIAG CLI", RunID: id, Version: version, Provenance: "native-job:" + id, CapturedAt: wire.CapturedAt, Data: append([]byte(nil), data...), Untrusted: true}, nil
}
