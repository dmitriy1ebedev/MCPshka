package releaseprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/cievidence"
)

const LocalSchema = "mcpshka.operator-verified-local/v1"

type TestEvidence struct {
	Kind       string            `json:"kind"`
	ID         string            `json:"id"`
	Tree       string            `json:"tree"`
	Conclusion string            `json:"conclusion"`
	Suites     map[string]string `json:"suites"`
}
type LocalReceipt struct {
	Schema         string       `json:"schema"`
	Provider       string       `json:"provider"`
	Repository     string       `json:"repository"`
	SourceSHA      string       `json:"source_sha"`
	HeadSHA        string       `json:"head_sha"`
	Tree           string       `json:"tree"`
	SourceDigest   string       `json:"source_digest"`
	BuildPermit    string       `json:"build_permit"`
	BuildEnvDigest string       `json:"build_env_digest"`
	BinarySHA      string       `json:"binary_sha256"`
	BrokerSHA      string       `json:"broker_binary_sha256"`
	TestEvidence   TestEvidence `json:"test_evidence"`
}

func ParseLocalReceipt(raw []byte) (LocalReceipt, error) {
	var r LocalReceipt
	if len(raw) > 8192 || strict(raw, &r) != nil {
		return r, errInvalid
	}
	if r.Schema != LocalSchema || r.Provider != "h96-local" || r.Repository != "dmitriy1ebedev/mcpshka" || !digest(r.SourceSHA, 40) || r.HeadSHA != r.SourceSHA || !digest(r.Tree, 40) || !digest(r.SourceDigest, 64) || !digest(r.BuildPermit, 32) || !digest(r.BuildEnvDigest, 64) || !digest(r.BinarySHA, 64) || !digest(r.BrokerSHA, 64) {
		return r, errInvalid
	}
	ev := r.TestEvidence
	if !digest(ev.ID, 32) || ev.Tree != r.Tree || ev.Conclusion != "success" {
		return r, errInvalid
	}
	suites := []string{"module"}
	switch ev.Kind {
	case "module":
	case "distributed":
		suites = []string{"shard-0", "shard-1", "shard-2"}
	case "gitverse":
		suites = []string{"release-suite", "snapshot-contracts"}
	case "github":
		suites = cievidence.RequiredChecks
	default:
		return r, errInvalid
	}
	if len(ev.Suites) != len(suites) {
		return r, errInvalid
	}
	for _, s := range suites {
		if ev.Suites[s] != "success" {
			return r, errInvalid
		}
	}
	return r, nil
}

type LocalRequest struct {
	Source         string `json:"source"`
	Version        string `json:"version"`
	CurrentVersion string `json:"current_version"`
	SourceDigest   string `json:"source_digest"`
	BuildPermit    string `json:"build_permit"`
	LaunchID       string `json:"launch_id"`
	TestPermit     string `json:"test_permit"`
}

func ParseLocalRequest(raw []byte) (LocalRequest, error) {
	var r LocalRequest
	if len(raw) > 8192 || strict(raw, &r) != nil || r.Source != "local" || !versionPattern.MatchString(r.Version) || !versionPattern.MatchString(r.CurrentVersion) || r.Version == r.CurrentVersion || !digest(r.SourceDigest, 64) || !digest(r.BuildPermit, 32) || !digest(r.LaunchID, 32) || !digest(r.TestPermit, 32) {
		return r, errInvalid
	}
	return r, nil
}

type localCall func(context.Context, string, ...string) ([]byte, error)

// A lost launch reply remains UNKNOWN. Re-entry probes the same permit and
// never launches it again once intent exists; successful results need fencing.
func awaitLocalBuild(ctx context.Context, r LocalRequest, call localCall) error {
	for {
		raw, err := call(ctx, "executor", "status", r.BuildPermit, r.LaunchID)
		if err != nil {
			return err
		}
		var s struct {
			State  string `json:"state"`
			Source *struct {
				Package       string `json:"package"`
				ReleaseDigest string `json:"release_digest"`
			} `json:"source"`
			Launches []json.RawMessage `json:"launches"`
			Result   *struct {
				State      string `json:"state"`
				ExitCode   int    `json:"exit_code"`
				WaitFailed bool   `json:"wait_failed"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &s) != nil || s.Source == nil || s.Source.Package != "release:paired" || s.Source.ReleaseDigest != r.SourceDigest {
			return errInvalid
		}
		if s.State == "FENCED" {
			if s.Result == nil || s.Result.State != "SUCCEEDED" || s.Result.ExitCode != 0 || s.Result.WaitFailed {
				return errors.New("local build not successful: HOLD")
			}
			return nil
		}
		if s.State != "ACTIVE" {
			return errInvalid
		}
		if len(s.Launches) == 0 {
			if _, err = call(ctx, "executor", "launch", r.BuildPermit, r.LaunchID); err != nil {
				return err
			}
		}
		if s.Result != nil {
			if _, err = call(ctx, "executor", "canary-fence", r.BuildPermit, r.LaunchID); err != nil {
				return err
			}
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// localPartsDir keeps partly read images between release_local calls: a
// client gives one tool call about a minute, while 46 MB in 32 KiB broker
// reads take longer, so every call was cancelled and restarted from zero
// (BUG-20261023-66). The final SHA-256 check covers the whole image.
var localPartsDir = "/var/lib/mcpshka/release-local"

// errCollecting reports an image still being read; the next call resumes.
var errCollecting = errors.New("image collection continues")

func readLocalImage(ctx context.Context, permit, name, want string, call localCall) ([]byte, error) {
	return readLocalImageUntil(ctx, permit, name, want, call, time.Time{})
}

// readLocalImageUntil resumes the image from its part file and stops with
// errCollecting at the deadline (zero: read to the end).
func readLocalImageUntil(ctx context.Context, permit, name, want string, call localCall, deadline time.Time) ([]byte, error) {
	if err := os.MkdirAll(localPartsDir, 0o700); err != nil {
		return nil, err
	}
	part := filepath.Join(localPartsDir, permit+"-"+name+".part")
	// A verified image survives the call that read it: the broker image did
	// not fit in the same call, the controller was dropped from memory and
	// read again forever (BUG-20261023-74).
	done := filepath.Join(localPartsDir, permit+"-"+name+".done")
	if old, _ := filepath.Glob(filepath.Join(localPartsDir, "*.done")); len(old) > 0 {
		for _, f := range old {
			if !strings.HasPrefix(filepath.Base(f), permit+"-") {
				os.Remove(f)
			}
		}
	}
	if data, err := os.ReadFile(done); err == nil {
		if sum(data) == want {
			return data, nil
		}
		os.Remove(done)
	}
	data, err := os.ReadFile(part)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, errCollecting
		}
		raw, err := call(ctx, "executor", "release-read", permit, name, strconv.Itoa(len(data)))
		if err != nil {
			return nil, err
		}
		var chunk struct {
			Data []byte `json:"data"`
			EOF  bool   `json:"eof"`
		}
		if json.Unmarshal(raw, &chunk) != nil || len(chunk.Data) > 32768 || len(data)+len(chunk.Data) > maxArchive || (!chunk.EOF && len(chunk.Data) == 0) {
			os.Remove(part)
			return nil, errInvalid
		}
		if _, err := f.Write(chunk.Data); err != nil {
			return nil, err
		}
		data = append(data, chunk.Data...)
		if chunk.EOF {
			break
		}
	}
	if sum(data) != want || len(data) < 64 || !bytes.Equal(data[:6], []byte("\x7fELF\x02\x01")) || binary.LittleEndian.Uint16(data[18:20]) != 183 {
		os.Remove(part)
		return nil, errInvalid
	}
	if err := os.Rename(part, done); err != nil {
		os.Remove(part)
	}
	return data, nil
}

func RunLocal(ctx context.Context, raw []byte) (map[string]any, error) {
	r, err := ParseLocalRequest(raw)
	if err != nil {
		return nil, err
	}
	return runLocal(ctx, r, production(), brokerclient.Run)
}
func runLocal(ctx context.Context, r LocalRequest, env environment, call localCall) (map[string]any, error) {
	if err := awaitLocalBuild(ctx, r, call); err != nil {
		return nil, err
	}
	return collectAndStage(ctx, r, env, call)
}

// LocalProgress is one step of a local release: the root broker only accepts
// the controller's own PID, so every broker call happens in the MCPshka
// process, one non-blocking step per tool call (release_local).
type LocalProgress struct {
	Stage    string         `json:"stage"`
	Detail   string         `json:"detail,omitempty"`
	Prepared map[string]any `json:"prepared,omitempty"`
	// NextAction names the cheapest next call. Once the build is fenced,
	// release_apply re-hashes the root-owned images itself, so collecting
	// is only needed for project_deploy (BUG-20261103-7).
	NextAction string `json:"next_action,omitempty"`
}

// collectedMiB reports how much of both images this permit already holds.
func collectedMiB(permit string) float64 {
	var n int64
	for _, name := range []string{"controller", "broker"} {
		for _, ext := range []string{".part", ".done"} {
			if st, err := os.Stat(filepath.Join(localPartsDir, permit+"-"+name+ext)); err == nil {
				n += st.Size()
			}
		}
	}
	return float64(n) / (1 << 20)
}

const localApplyHint = "release_apply (build is fenced; root re-hashes the images, no collection needed); or keep calling release_local with a new key to stage for project_deploy"

func StepLocal(ctx context.Context, raw []byte) (LocalProgress, error) {
	r, err := ParseLocalRequest(raw)
	if err != nil {
		return LocalProgress{}, err
	}
	return stepLocal(ctx, r, production(), brokerclient.Run)
}

func stepLocal(ctx context.Context, r LocalRequest, env environment, call localCall) (LocalProgress, error) {
	raw, err := call(ctx, "executor", "status", r.BuildPermit, r.LaunchID)
	if err != nil {
		return LocalProgress{}, err
	}
	var s struct {
		State string `json:"state"`
		Queue *struct {
			State    string `json:"state"`
			Reason   string `json:"reason"`
			Position int    `json:"position"`
		} `json:"queue,omitempty"`
		Source *struct {
			Package       string `json:"package"`
			ReleaseDigest string `json:"release_digest"`
		} `json:"source"`
		Launches []json.RawMessage `json:"launches"`
		Result   *struct {
			State      string `json:"state"`
			ExitCode   int    `json:"exit_code"`
			WaitFailed bool   `json:"wait_failed"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Source == nil || s.Source.Package != "release:paired" || s.Source.ReleaseDigest != r.SourceDigest {
		return LocalProgress{}, errInvalid
	}
	switch {
	case s.State == "ACTIVE" && s.Result == nil && s.Queue != nil && s.Queue.State == "QUEUED":
		return LocalProgress{Stage: "QUEUED", Detail: fmt.Sprintf("queue position %d: %s", s.Queue.Position, s.Queue.Reason)}, nil
	case s.State == "ACTIVE" && len(s.Launches) == 0:
		if _, err = call(ctx, "executor", "launch", r.BuildPermit, r.LaunchID); err != nil {
			return LocalProgress{}, err
		}
		return LocalProgress{Stage: "BUILD_LAUNCHED", Detail: "sealed paired build started"}, nil
	case s.State == "ACTIVE" && s.Result == nil:
		return LocalProgress{Stage: "BUILD_RUNNING", Detail: "sealed paired build is running"}, nil
	case s.State == "ACTIVE":
		if _, err = call(ctx, "executor", "canary-fence", r.BuildPermit, r.LaunchID); err != nil {
			return LocalProgress{}, err
		}
		return LocalProgress{Stage: "BUILD_FENCING", Detail: "build result recorded; fence requested"}, nil
	case s.State != "FENCED":
		return LocalProgress{}, errInvalid
	case s.Result == nil || s.Result.State != "SUCCEEDED" || s.Result.ExitCode != 0 || s.Result.WaitFailed:
		return LocalProgress{}, errors.New("local build not successful: HOLD")
	}
	// Staging rewrites identical bytes idempotently, so a repeat is a no-op.
	prepared, err := collectAndStageBy(ctx, r, env, call, time.Now().Add(localStepBudget))
	if errors.Is(err, errCollecting) {
		return LocalProgress{Stage: "COLLECTING", Detail: fmt.Sprintf("reading the built images (%.1f MiB collected); call release_local again with a new key", collectedMiB(r.BuildPermit)), NextAction: localApplyHint}, nil
	}
	if err != nil {
		return LocalProgress{}, err
	}
	return LocalProgress{Stage: "STAGED", Detail: "candidate, broker, plan, receipt and coordinated manifest staged", Prepared: prepared, NextAction: "release_apply, or project_deploy with the staged candidate"}, nil
}

// localStepBudget keeps one release_local call inside a chat client's
// tool timeout.
var localStepBudget = 40 * time.Second

func collectAndStage(ctx context.Context, r LocalRequest, env environment, call localCall) (map[string]any, error) {
	return collectAndStageBy(ctx, r, env, call, time.Time{})
}

func collectAndStageBy(ctx context.Context, r LocalRequest, env environment, call localCall, deadline time.Time) (map[string]any, error) {
	raw, err := call(ctx, "executor", "release-receipt", r.BuildPermit, r.TestPermit)
	if err != nil {
		return nil, err
	}
	c, err := ParseLocalReceipt(raw)
	if err != nil || c.BuildPermit != r.BuildPermit || c.TestEvidence.ID != r.TestPermit || c.SourceDigest != r.SourceDigest {
		return nil, errInvalid
	}
	image, err := readLocalImageUntil(ctx, r.BuildPermit, "controller", c.BinarySHA, call, deadline)
	if err != nil {
		return nil, err
	}
	broker, err := readLocalImageUntil(ctx, r.BuildPermit, "broker", c.BrokerSHA, call, deadline)
	if err != nil {
		return nil, err
	}
	var receiptMap map[string]any
	if json.Unmarshal(canonical(c), &receiptMap) != nil {
		return nil, errInvalid
	}
	req := request{Version: r.Version, CurrentVersion: r.CurrentVersion, SourceDigest: r.SourceDigest, Receipt: receipt{SourceSHA: c.SourceSHA, Tree: c.Tree, BinarySHA: c.BinarySHA, BrokerSHA: c.BrokerSHA, ArchiveSHA: c.BuildEnvDigest}}
	return stage(image, broker, identity{Schema: "mcpshka.source-identity/v1", SourceDigest: c.SourceDigest}, req, env, canonical(receiptMap))
}
