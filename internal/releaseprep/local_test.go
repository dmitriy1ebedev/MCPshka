package releaseprep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/cievidence"
)

func TestLocalReceiptStrict(t *testing.T) {
	raw := []byte(`{"schema":"mcpshka.operator-verified-local/v1","provider":"h96-local","repository":"dmitriy1ebedev/mcpshka","source_sha":"` + strings.Repeat("a", 40) + `","head_sha":"` + strings.Repeat("a", 40) + `","tree":"` + strings.Repeat("b", 40) + `","source_digest":"` + strings.Repeat("c", 64) + `","build_permit":"` + strings.Repeat("d", 32) + `","build_env_digest":"` + strings.Repeat("e", 64) + `","binary_sha256":"` + strings.Repeat("f", 64) + `","broker_binary_sha256":"` + strings.Repeat("1", 64) + `","test_evidence":{"kind":"module","id":"` + strings.Repeat("2", 32) + `","tree":"` + strings.Repeat("b", 40) + `","conclusion":"success","suites":{"module":"success"}}}`)
	if _, err := ParseLocalReceipt(raw); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(raw, &v)
	for _, key := range []string{"extra", "tree", "build_permit", "build_env_digest", "binary_sha256", "head_sha"} {
		json.Unmarshal(raw, &v)
		v[key] = "invalid"
		if _, err := ParseLocalReceipt(canonical(v)); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	for _, key := range []string{"extra", "kind", "tree", "conclusion", "id", "suites"} {
		json.Unmarshal(raw, &v)
		v["test_evidence"].(map[string]any)[key] = "invalid"
		if _, err := ParseLocalReceipt(canonical(v)); err == nil {
			t.Fatalf("accepted evidence %s", key)
		}
	}
	if _, err := ParseLocalReceipt([]byte(strings.Replace(string(raw), `"provider":"h96-local"`, `"provider":"github","provider":"h96-local"`, 1))); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestLocalPreparationProducesGateCompatiblePair(t *testing.T) {
	archive, ci := fixture(t, nil, false)
	image, broker, _, err := unpack(archive, ci)
	if err != nil {
		t.Fatal(err)
	}
	in := LocalRequest{Source: "local", Version: "0.1.169", CurrentVersion: "0.1.168", SourceDigest: ci.SourceDigest, BuildPermit: strings.Repeat("d", 32), LaunchID: strings.Repeat("e", 32), TestPermit: strings.Repeat("f", 32)}
	receipt := LocalReceipt{Schema: LocalSchema, Provider: "h96-local", Repository: "dmitriy1ebedev/mcpshka", SourceSHA: ci.Receipt.SourceSHA, HeadSHA: ci.Receipt.SourceSHA, Tree: ci.Receipt.Tree, SourceDigest: ci.SourceDigest, BuildPermit: in.BuildPermit, BuildEnvDigest: strings.Repeat("1", 64), BinarySHA: sum(image), BrokerSHA: sum(broker), TestEvidence: TestEvidence{Kind: "module", ID: in.TestPermit, Tree: ci.Receipt.Tree, Conclusion: "success", Suites: map[string]string{"module": "success"}}}
	calls := func(_ context.Context, family string, args ...string) ([]byte, error) {
		switch args[0] {
		case "status":
			return canonical(map[string]any{"state": "FENCED", "source": map[string]string{"package": "release:paired", "release_digest": in.SourceDigest}, "result": map[string]any{"state": "SUCCEEDED", "exit_code": 0, "wait_failed": false}}), nil
		case "release-receipt":
			return canonical(receipt), nil
		case "release-read":
			data := image
			if args[2] == "broker" {
				data = broker
			}
			return canonical(map[string]any{"data": data, "eof": true}), nil
		default:
			t.Fatalf("unexpected build replay: %v", args)
			return nil, nil
		}
	}
	env := stagingFixture(t)
	out, err := runLocal(context.Background(), in, env, calls)
	if err != nil {
		t.Fatal(err)
	}
	candidate := out["candidate"].(string)
	script := `import json,runpy,sys; g=runpy.run_path(sys.argv[1]); g['verify_local_authority'].__globals__['verify_local_authority']=lambda r: None; p=json.load(open(sys.argv[2])); r=json.load(open(sys.argv[3])); g['validate_plan'](p); g['validate_receipt'](r,p); assert p['ci_run']==0; assert g['sha'](g['canonical'](r))==p['ci_receipt_sha256']`
	if raw, err := exec.Command("python3", "-I", "-S", "-c", script, "../../packaging/native/release-gate.py", candidate+".release-plan.json", candidate+".ci-receipt.json").CombinedOutput(); err != nil {
		t.Fatalf("gate: %s %v", raw, err)
	}
	receipt.BrokerSHA = strings.Repeat("0", 64)
	if _, err = runLocal(context.Background(), in, env, calls); err == nil {
		t.Fatal("changed broker accepted")
	}
}

func TestLocalBuildDoesNotReplayUnknownOrAcceptOtherPurpose(t *testing.T) {
	in := LocalRequest{SourceDigest: strings.Repeat("a", 64)}
	for _, state := range []string{`{"state":"ACTIVE","launches":[{}]}`, `{"state":"FENCED","result":{"state":"FAILED"}}`, `{"state":"ACTIVE","source":{"package":"module:all"}}`} {
		err := awaitLocalBuild(context.Background(), in, func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] != "status" {
				t.Fatal("unexpected launch", args)
			}
			return []byte(state), nil
		})
		if err == nil {
			t.Fatal("unbound state accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	count := 0
	err := awaitLocalBuild(ctx, in, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		count++
		if args[0] != "status" {
			t.Fatal("relaunch", args)
		}
		return canonical(map[string]any{"state": "ACTIVE", "source": map[string]string{"package": "release:paired", "release_digest": in.SourceDigest}, "launches": []any{map[string]any{}}}), nil
	})
	if err != context.Canceled || count != 1 {
		t.Fatal("UNKNOWN did not remain non-replayed", err, count)
	}
}

func TestStepLocalWalksBuildToStagedOnce(t *testing.T) {
	archive, ci := fixture(t, nil, false)
	image, broker, _, err := unpack(archive, ci)
	if err != nil {
		t.Fatal(err)
	}
	in := LocalRequest{Source: "local", Version: "0.1.169", CurrentVersion: "0.1.168", SourceDigest: ci.SourceDigest, BuildPermit: strings.Repeat("d", 32), LaunchID: strings.Repeat("e", 32), TestPermit: strings.Repeat("f", 32)}
	receipt := LocalReceipt{Schema: LocalSchema, Provider: "h96-local", Repository: "dmitriy1ebedev/mcpshka", SourceSHA: ci.Receipt.SourceSHA, HeadSHA: ci.Receipt.SourceSHA, Tree: ci.Receipt.Tree, SourceDigest: ci.SourceDigest, BuildPermit: in.BuildPermit, BuildEnvDigest: strings.Repeat("1", 64), BinarySHA: sum(image), BrokerSHA: sum(broker), TestEvidence: TestEvidence{Kind: "module", ID: in.TestPermit, Tree: ci.Receipt.Tree, Conclusion: "success", Suites: map[string]string{"module": "success"}}}
	launched, result, fenced, launches := false, false, false, 0
	calls := func(_ context.Context, family string, args ...string) ([]byte, error) {
		src := map[string]string{"package": "release:paired", "release_digest": in.SourceDigest}
		switch args[0] {
		case "status":
			st := map[string]any{"state": "ACTIVE", "source": src, "launches": []any{}}
			if launched {
				st["launches"] = []any{map[string]any{}}
			}
			if result {
				st["result"] = map[string]any{"state": "EXITED_UNFENCED", "exit_code": 0}
			}
			if fenced {
				st["state"], st["result"] = "FENCED", map[string]any{"state": "SUCCEEDED", "exit_code": 0, "wait_failed": false}
			}
			return canonical(st), nil
		case "launch":
			launched, launches = true, launches+1
		case "canary-fence":
			fenced = true
		case "release-receipt":
			return canonical(receipt), nil
		case "release-read":
			data := image
			if args[2] == "broker" {
				data = broker
			}
			return canonical(map[string]any{"data": data, "eof": true}), nil
		}
		return []byte("{}"), nil
	}
	env := stagingFixture(t)
	step := func(want string) LocalProgress {
		t.Helper()
		out, err := stepLocal(context.Background(), in, env, calls)
		if err != nil || out.Stage != want {
			t.Fatalf("want %s, got %+v %v", want, out, err)
		}
		return out
	}
	step("BUILD_LAUNCHED")
	step("BUILD_RUNNING")
	result = true
	step("BUILD_FENCING")
	staged := step("STAGED")
	again := step("STAGED")
	if staged.NextAction == "" {
		t.Fatal("STAGED without next_action")
	}
	if launches != 1 || staged.Prepared["candidate"] == nil || again.Prepared["candidate"] != staged.Prepared["candidate"] {
		t.Fatalf("not staged exactly once: launches=%d %+v", launches, staged)
	}
	fenced, receipt.BrokerSHA = true, strings.Repeat("0", 64)
	if _, err := stepLocal(context.Background(), in, env, calls); err == nil {
		t.Fatal("changed broker accepted")
	}
}

// A chat client cancels a tool call after about a minute; image collection
// resumes across calls instead of restarting (BUG-20261023-66).
func TestLocalImageCollectionResumesAcrossCalls(t *testing.T) {
	stagingFixture(t)
	image := append([]byte("\x7fELF\x02\x01"), make([]byte, 100000)...)
	image[18] = 183
	reads := 0
	call := func(_ context.Context, family string, args ...string) ([]byte, error) {
		reads++
		off, _ := strconv.Atoi(args[3])
		end := min(off+32768, len(image))
		return canonical(map[string]any{"data": image[off:end], "eof": end == len(image)}), nil
	}
	past := time.Now().Add(-time.Second)
	// An expired budget still leaves the read position where it was.
	if _, err := readLocalImageUntil(context.Background(), strings.Repeat("d", 32), "controller", sum(image), call, past); !errors.Is(err, errCollecting) || reads != 0 {
		t.Fatal(err, reads)
	}
	// One chunk per call, then the rest; the part file carries the offset.
	first := 0
	step := func(_ context.Context, family string, args ...string) ([]byte, error) {
		if first++; first > 1 {
			return nil, context.DeadlineExceeded
		}
		return call(context.Background(), family, args...)
	}
	if _, err := readLocalImageUntil(context.Background(), strings.Repeat("d", 32), "controller", sum(image), step, time.Time{}); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	got, err := readLocalImageUntil(context.Background(), strings.Repeat("d", 32), "controller", sum(image), call, time.Time{})
	if err != nil || !bytes.Equal(got, image) || reads != 4 {
		t.Fatal("resumed image", err, reads)
	}
	if _, err := os.Stat(filepath.Join(localPartsDir, strings.Repeat("d", 32)+"-controller.part")); !os.IsNotExist(err) {
		t.Fatal("part file kept after a verified image", err) // renamed to .done
	}
}

// The verified controller must survive into the call that finishes the
// broker; it was read again from zero forever (BUG-20261023-74).
func TestVerifiedImageSurvivesTheCall(t *testing.T) {
	stagingFixture(t)
	image := append([]byte("\x7fELF\x02\x01"), make([]byte, 40000)...)
	image[18] = 183
	call := func(_ context.Context, family string, args ...string) ([]byte, error) {
		off, _ := strconv.Atoi(args[3])
		end := min(off+32768, len(image))
		return canonical(map[string]any{"data": image[off:end], "eof": end == len(image)}), nil
	}
	permit := strings.Repeat("e", 32)
	if _, err := readLocalImageUntil(context.Background(), permit, "controller", sum(image), call, time.Time{}); err != nil {
		t.Fatal(err)
	}
	broken := func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("must not read again")
	}
	got, err := readLocalImageUntil(context.Background(), permit, "controller", sum(image), broken, time.Time{})
	if err != nil || !bytes.Equal(got, image) {
		t.Fatal("verified image read again", err)
	}
}

func TestLocalProgressReportsBuildQueue(t *testing.T) {
	r := LocalRequest{SourceDigest: strings.Repeat("a", 64)}
	call := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != "status" {
			t.Fatalf("queued build attempted mutation: %v", args)
		}
		return json.Marshal(map[string]any{"state": "ACTIVE", "source": map[string]string{"package": "release:paired", "release_digest": r.SourceDigest}, "launches": []any{map[string]string{"state": "REGISTERED"}}, "queue": map[string]any{"state": "QUEUED", "reason": "load1 exceeds limit", "position": 2}})
	}
	out, err := stepLocal(context.Background(), r, environment{}, call)
	if err != nil || out.Stage != "QUEUED" || out.Detail != "queue position 2: load1 exceeds limit" {
		t.Fatalf("%+v %v", out, err)
	}
}

// COLLECTING names release_apply and how much is read (BUG-20261103-7).
func TestCollectedMiBCountsPartAndDone(t *testing.T) {
	stagingFixture(t)
	permit := strings.Repeat("e", 32)
	os.MkdirAll(localPartsDir, 0o700)
	os.WriteFile(filepath.Join(localPartsDir, permit+"-controller.done"), make([]byte, 1<<20), 0o600)
	os.WriteFile(filepath.Join(localPartsDir, permit+"-broker.part"), make([]byte, 1<<19), 0o600)
	if got := collectedMiB(permit); got != 1.5 {
		t.Fatalf("collected %.2f MiB, want 1.5", got)
	}
	if !strings.Contains(localApplyHint, "release_apply") {
		t.Fatal(localApplyHint)
	}
}

// 0.9.8 fallback: GitHub evidence counts only with its full required check
// set, and the root gate (release-gate.py) names the same checks.
func TestLocalReceiptAcceptsFullGitHubEvidence(t *testing.T) {
	base := `{"schema":"mcpshka.operator-verified-local/v1","provider":"h96-local","repository":"dmitriy1ebedev/mcpshka","source_sha":"` + strings.Repeat("a", 40) + `","head_sha":"` + strings.Repeat("a", 40) + `","tree":"` + strings.Repeat("b", 40) + `","source_digest":"` + strings.Repeat("c", 64) + `","build_permit":"` + strings.Repeat("d", 32) + `","build_env_digest":"` + strings.Repeat("e", 64) + `","binary_sha256":"` + strings.Repeat("f", 64) + `","broker_binary_sha256":"` + strings.Repeat("1", 64) + `","test_evidence":{"kind":"github","id":"` + strings.Repeat("2", 32) + `","tree":"` + strings.Repeat("b", 40) + `","conclusion":"success","suites":SUITES}}`
	suites := map[string]string{}
	for _, c := range cievidence.RequiredChecks {
		suites[c] = "success"
	}
	full, _ := json.Marshal(suites)
	if _, err := ParseLocalReceipt([]byte(strings.Replace(base, "SUITES", string(full), 1))); err != nil {
		t.Fatal(err)
	}
	delete(suites, cievidence.RequiredChecks[0])
	part, _ := json.Marshal(suites)
	if _, err := ParseLocalReceipt([]byte(strings.Replace(base, "SUITES", string(part), 1))); err == nil {
		t.Fatal("incomplete GitHub evidence accepted")
	}
	gate, err := os.ReadFile("../../packaging/native/release-gate.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cievidence.RequiredChecks {
		if !strings.Contains(string(gate), "'"+c+"'") {
			t.Fatalf("release-gate.py LOCAL_TEST_SUITES lacks %q", c)
		}
	}
}

func TestDistributedLocalReceiptRequiresAllShards(t *testing.T) {
	raw := []byte(`{"schema":"mcpshka.operator-verified-local/v1","provider":"h96-local","repository":"dmitriy1ebedev/mcpshka","source_sha":"` + strings.Repeat("a", 40) + `","head_sha":"` + strings.Repeat("a", 40) + `","tree":"` + strings.Repeat("b", 40) + `","source_digest":"` + strings.Repeat("c", 64) + `","build_permit":"` + strings.Repeat("d", 32) + `","build_env_digest":"` + strings.Repeat("e", 64) + `","binary_sha256":"` + strings.Repeat("f", 64) + `","broker_binary_sha256":"` + strings.Repeat("1", 64) + `","test_evidence":{"kind":"distributed","id":"` + strings.Repeat("2", 32) + `","tree":"` + strings.Repeat("b", 40) + `","conclusion":"success","suites":{"shard-0":"success","shard-1":"success","shard-2":"success","shard-3":"success"}}}`)
	if _, e := ParseLocalReceipt(raw); e != nil {
		t.Fatal(e)
	}
	raw = []byte(strings.Replace(string(raw), `,"shard-3":"success"`, "", 1))
	if _, e := ParseLocalReceipt(raw); e == nil {
		t.Fatal("partial distributed receipt passed")
	}
}
