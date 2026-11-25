package releaseprep

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func fixture(t *testing.T, change func(map[string][]byte), duplicate bool) ([]byte, request) {
	t.Helper()
	image := append([]byte("\x7fELF\x02\x01"), make([]byte, 90)...)
	binary.LittleEndian.PutUint16(image[18:20], 183)
	broker := append(append([]byte(nil), image...), []byte("broker")...)
	r := request{Version: "0.1.145", CurrentVersion: "0.1.143", SourceDigest: strings.Repeat("b", 64), Receipt: receipt{Schema: "mcpshka.operator-verified-ci/v2", Provider: "github", Repository: "dmitriy1ebedev/mcpshka", RunID: 9007199254740993, ArtifactID: 12141143451, SourceSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("e", 40), Tree: strings.Repeat("c", 40), SourceDigest: strings.Repeat("b", 64), Conclusion: "success", Suites: map[string]string{"release": "success", "race": "success", "fuzz": "success", "build": "success", "release-arm64": "success"}}}
	values := map[string][]byte{
		"checks.log": []byte("PASS"), "mcpshka-linux-arm64": image, "mcpshka-broker-linux-arm64": broker,
		"release-arm64-buildinfo.txt": []byte("GOARCH=arm64\nCGO_ENABLED=1\n"), "broker-arm64-buildinfo.txt": []byte("GOARCH=arm64\nCGO_ENABLED=1\n"),
		"release-arm64-sha256.txt": []byte(sum(image) + "  /runner/mcpshka-linux-arm64\n"), "broker-arm64-sha256.txt": []byte(sum(broker) + "  mcpshka-broker-linux-arm64\n"),
		"source-sha.txt": []byte(r.Receipt.SourceSHA + "\n"), "source-tree.txt": []byte(r.Receipt.Tree + "\n"), "toolchain.txt": []byte("go1.26"),
		"source-identity.json": canonical(identity{Schema: "mcpshka.source-identity/v1", SourceDigest: r.SourceDigest, FileCount: 1, TotalBytes: 96}),
	}
	if change != nil {
		change(values)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, data := range values {
		f, e := writer.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if duplicate {
		f, _ := writer.Create("checks.log")
		f.Write([]byte("override"))
	}
	if e := writer.Close(); e != nil {
		t.Fatal(e)
	}
	raw := buf.Bytes()
	r.Receipt.ArchiveSHA = sum(raw)
	return raw, r
}

func TestArchiveRequiresExactSourceAndPairedImages(t *testing.T) {
	raw, r := fixture(t, nil, false)
	image, broker, id, e := unpack(raw, r)
	if e != nil || len(image) < 64 || len(broker) < 64 || id.SourceDigest != r.SourceDigest {
		t.Fatalf("good fixture rejected: %v", e)
	}
	for name, edit := range map[string]func(*request){
		"source": func(r *request) { r.Receipt.SourceSHA = strings.Repeat("d", 40) }, "tree": func(r *request) { r.Receipt.Tree = strings.Repeat("d", 40) },
		"digest": func(r *request) { r.SourceDigest = strings.Repeat("d", 64) }, "archive": func(r *request) { r.Receipt.ArchiveSHA = strings.Repeat("d", 64) },
		"controller": func(r *request) { r.Receipt.BinarySHA = strings.Repeat("d", 64) }, "broker": func(r *request) { r.Receipt.BrokerSHA = strings.Repeat("d", 64) },
		"schema": func(r *request) { r.Receipt.Schema = "mcpshka.operator-verified-ci/v1" },
	} {
		t.Run(name, func(t *testing.T) {
			other := r
			edit(&other)
			if _, _, _, e := unpack(raw, other); e == nil {
				t.Fatal("unbound archive accepted")
			}
		})
	}
}
func TestArchiveRejectsAliasesBombsWrongArchAndDuplicates(t *testing.T) {
	changes := map[string]func(map[string][]byte){
		"extra":          func(v map[string][]byte) { v["../outside"] = []byte("bad") },
		"missing":        func(v map[string][]byte) { delete(v, "mcpshka-broker-linux-arm64") },
		"arch":           func(v map[string][]byte) { v["mcpshka-linux-arm64"][18] = 0 },
		"build":          func(v map[string][]byte) { v["broker-arm64-buildinfo.txt"] = []byte("CGO_ENABLED=0") },
		"metadata_limit": func(v map[string][]byte) { v["checks.log"] = bytes.Repeat([]byte("x"), 65537) },
		"duplicate_json": func(v map[string][]byte) {
			v["source-identity.json"] = []byte(`{"schema":"wrong","schema":"mcpshka.source-identity/v1","source_digest":"` + strings.Repeat("b", 64) + `","file_count":1,"total_bytes":96}`)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			raw, r := fixture(t, change, false)
			if _, _, _, e := unpack(raw, r); e == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	raw, r := fixture(t, nil, true)
	if _, _, _, e := unpack(raw, r); e == nil {
		t.Fatal("duplicate ZIP entry accepted")
	}
	raw, r = fixture(t, nil, false)
	raw = append(raw, 0)
	r.Receipt.ArchiveSHA = sum(raw)
	if _, _, _, e := unpack(raw, r); e == nil {
		t.Fatal("trailing archive data accepted")
	}
}
func TestRequestRequiresAllReleaseGatesAndStrictJSON(t *testing.T) {
	_, r := fixture(t, nil, false)
	raw := canonical(r)
	if _, e := parse(raw); e != nil {
		t.Fatal(e)
	}
	for _, suite := range []string{"release", "race", "fuzz", "build", "release-arm64"} {
		r.Receipt.Suites[suite] = "unknown"
		if _, e := parse(canonical(r)); e == nil {
			t.Fatal("unknown CI accepted")
		}
		r.Receipt.Suites[suite] = "success"
	}
	for _, bad := range [][]byte{append(append([]byte(nil), raw...), []byte("{}")...), []byte(strings.Replace(string(raw), `"version":`, `"version":"0.1.146","version":`, 1)), []byte(strings.Replace(string(raw), `"run_id":9007199254740993`, `"run_id":true`, 1)), []byte(strings.Replace(string(raw), `"current_version":`, `"extra":0,"current_version":`, 1))} {
		if _, e := parse(bad); e == nil {
			t.Fatalf("invalid request accepted: %.50s", bad)
		}
	}
	for _, bad := range []string{"http://files.oaiusercontent.com/a", "https://127.0.0.1/a", "https://files.oaiusercontent.com.evil/a", "https://x@files.oaiusercontent.com/a", "https://files.oaiusercontent.com:444/a", "https://files.oaiusercontent.com/a#x", "https://files.oaiusercontent.com/ a"} {
		if validateURL(bad) == nil {
			t.Fatalf("unsafe URL accepted %s", bad)
		}
	}
	if validateURL("https://files.oaiusercontent.com/a?sig=private") != nil {
		t.Fatal("valid artifact source rejected")
	}
}

func TestJSONRejectsCaseAliasesBeforeTypedDecoding(t *testing.T) {
	_, r := fixture(t, nil, false)
	raw := string(canonical(r))
	for _, bad := range []string{
		strings.Replace(raw, `"version":`, `"Version":"0.1.146","version":`, 1),
		strings.Replace(raw, `"provider":"github"`, `"provider":"wrong","Provider":"github"`, 1),
		strings.Replace(raw, `"provider":"github"`, `"Provider":"github"`, 1),
	} {
		if _, err := parse([]byte(bad)); err == nil {
			t.Fatal("case alias accepted")
		}
	}
	var id identity
	if strict([]byte(`{"Schema":"mcpshka.source-identity/v1","source_digest":"`+r.SourceDigest+`","file_count":1,"total_bytes":96}`), &id) == nil {
		t.Fatal("identity case alias accepted")
	}
}
func TestStagingResumesOnlyIdenticalPrivateFilesWithoutFollowingAliases(t *testing.T) {
	dir := t.TempDir()
	root, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	for range 2 {
		if e := writeVerified(root, "candidate", []byte("good"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := writeVerified(root, "candidate", []byte("evil"), 0700); e == nil {
		t.Fatal("different image overwrote candidate")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("keep"), 0600)
	os.Symlink(outside, filepath.Join(dir, "alias"))
	if e := writeVerified(root, "alias", []byte("keep"), 0700); e == nil {
		t.Fatal("symlink was followed")
	}
	os.Link(outside, filepath.Join(dir, "hardlink"))
	if e := writeVerified(root, "hardlink", []byte("keep"), 0700); e == nil {
		t.Fatal("hardlink accepted")
	}
	os.Chmod(filepath.Join(dir, "candidate"), 0644)
	if e := writeVerified(root, "candidate", []byte("good"), 0700); e == nil {
		t.Fatal("public candidate accepted on resume")
	}
	st, _ := os.Stat(outside)
	if st.Mode().Perm() != 0600 {
		t.Fatal("alias changed outside permissions")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "keep" {
		t.Fatal("alias changed outside bytes")
	}
}
func stagingFixture(t *testing.T) environment {
	t.Helper()
	previous := artifactStatfs
	artifactStatfs = func(_ string, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 10 << 18}
		return nil
	}
	t.Cleanup(func() { artifactStatfs = previous })
	parts := localPartsDir
	localPartsDir = t.TempDir()
	t.Cleanup(func() { localPartsDir = parts })
	repo := t.TempDir()
	os.Mkdir(filepath.Join(repo, "bin"), 0700)
	os.MkdirAll(filepath.Join(repo, "packaging", "native"), 0700)
	for _, name := range []string{"coordinated-upgrade.py", "release-gate.py", "self-deploy.sh"} {
		os.WriteFile(filepath.Join(repo, "packaging", "native", name), []byte("reviewed fixture "+name), 0600)
	}
	env := environment{repo: repo, owner: uint32(os.Geteuid())}
	for name, path := range map[string]*string{"controller": &env.controller, "broker": &env.broker, "guarded": &env.guarded, "gate": &env.gate} {
		*path = filepath.Join(repo, name)
		os.WriteFile(*path, []byte("installed "+name), 0600)
	}
	return env
}
func TestPreparationProducesGateCompatiblePairedManifestAndSurvivesRestart(t *testing.T) {
	raw, r := fixture(t, nil, false)
	env := stagingFixture(t)
	var first []byte
	for range 2 {
		out, e := prepare(raw, r, env)
		if e != nil {
			t.Fatal(e)
		}
		if out["deploy_authorized"] != false || out["trust"] != "UNTRUSTED" {
			t.Fatal("preparation granted authority")
		}
		candidate := out["candidate"].(string)
		plan, e := os.ReadFile(candidate + ".release-plan.json")
		if e != nil {
			t.Fatal(e)
		}
		if first != nil && !bytes.Equal(first, plan) {
			t.Fatal("restart changed plan")
		}
		first = plan
		ci, _ := os.ReadFile(candidate + ".ci-receipt.json")
		if !bytes.Contains(ci, []byte(`"run_id":9007199254740993`)) {
			t.Fatal("CI integer identity rounded")
		}
		var fields map[string]json.RawMessage
		json.Unmarshal(plan, &fields)
		var receiptSHA string
		json.Unmarshal(fields["ci_receipt_sha256"], &receiptSHA)
		if receiptSHA != sum(ci) {
			t.Fatal("receipt digest not bound")
		}
		script := `import json,runpy,sys; g=runpy.run_path(sys.argv[1]); p=g['strict_json'](open(sys.argv[2],'rb').read()); c=g['strict_json'](open(sys.argv[3],'rb').read()); g['validate_plan'](p); g['validate_receipt'](c,p); assert g['sha'](g['canonical'](p))==sys.argv[4]; assert g['sha'](g['canonical'](c))==p['ci_receipt_sha256']`
		// Cross-language compatibility is checked against the actual unchanged root gate.
		gate := filepath.Join("..", "..", "packaging", "native", "release-gate.py")
		if result, e := exec.Command("python3", "-I", "-S", "-c", script, gate, candidate+".release-plan.json", candidate+".ci-receipt.json", fmt.Sprint(out["plan_sha256"])).CombinedOutput(); e != nil {
			t.Fatalf("root gate rejected native manifest: %s %v", result, e)
		}
		coordinated := filepath.Join("..", "..", "packaging", "native", "coordinated-upgrade.py")
		paired := filepath.Join(env.repo, "bin", "mcpshka-coordinated-release-"+r.Version+".json")
		if result, e := exec.Command("python3", "-I", "-S", "-c", `import runpy,sys; from pathlib import Path; g=runpy.run_path(sys.argv[1]); g['validate'].__globals__['BIN']=Path(sys.argv[3]); g['validate'](g['parse'](open(sys.argv[2],'rb').read()))`, coordinated, paired, filepath.Join(env.repo, "bin")).CombinedOutput(); e != nil {
			t.Fatalf("coordinated gate rejected native manifest: %s %v", result, e)
		}
	}
	// A bin symlink must not redirect native staging outside the project.
	alias := stagingFixture(t)
	os.Remove(filepath.Join(alias.repo, "bin"))
	os.Symlink(filepath.Join(env.repo, "bin"), filepath.Join(alias.repo, "bin"))
	if _, e := prepare(raw, r, alias); e == nil {
		t.Fatal("aliased staging root accepted")
	}
}

func TestPreparationDiskReserveFailure(t *testing.T) {
	env := stagingFixture(t)
	for _, fault := range []error{nil, syscall.EIO} {
		artifactStatfs = func(_ string, st *syscall.Statfs_t) error {
			*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 2 << 18}
			return fault
		}
		if _, err := prepare(nil, request{}, env); err == nil || !strings.Contains(err.Error(), "disk reserve insufficient") {
			t.Fatal("prepare admitted low or unknown disk", err)
		}
		if _, err := stage(nil, nil, identity{}, request{}, env, nil); err == nil || !strings.Contains(err.Error(), "disk reserve insufficient") {
			t.Fatal("stage admitted low or unknown disk", err)
		}
	}
}
