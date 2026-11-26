package executor

import (
	"encoding/json"
	"mcpshka/internal/releaseprep"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalReleaseProfileFixedOfflinePair(t *testing.T) {
	command := []string{"/bin/sh", "-ec", localBuildScript}
	if !strings.Contains(localBuildScript, "build-runner-assets.sh /scratch/runner-assets") {
		t.Fatal("paired build omits downloadable runners")
	}
	s := RunSpec{Profile: "local-release-v1", Command: command, WorkspaceDevice: 1, WorkspaceInode: 1}
	if err := s.validate(); err != nil {
		t.Fatal("fixed paired build rejected", err)
	}
	args := strings.Join(workerArgs("", s), " ")
	for _, want := range []string{"--unshare-all", "--ro-bind-fd 3 /workspace", "GOPROXY off", "GOSUMDB off", "GOFLAGS -mod=readonly", "GOOS linux", "GOARCH arm64", "CGO_ENABLED 1", "GOMODCACHE /workspace/.gomodcache"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s", want)
		}
	}
	s.Command[2] += "\ntrue"
	if s.validate() == nil {
		t.Fatal("caller command accepted")
	}
	if integrationTestPurpose("project-local-release-v1") {
		t.Fatal("build accepted as test")
	}
}

func TestLocalReleaseSourceDigestMatchesCloud(t *testing.T) {
	base := t.TempDir()
	files := map[string]string{"go.mod": "module demo\n", "cmd/main.go": "package main\n", ".github/workflows/cloud.yml": "jobs: {}\n", "skills/demo.md": "example\n", ".gomodcache/cache": "not source", "docs/guide.md": "not release source\n", "README.md": "readme\n"}
	for name, raw := range files {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := releaseSourceDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	// Computed by packaging/cloud/source_digest.py for this committed fixture.
	const want = "085aa7f94a1ad31a32669aaae011c7425c4ad2245778764c22839a34f2bff2e5"
	if got != want {
		t.Fatalf("cloud source framing changed: %s", got)
	}
	os.WriteFile(filepath.Join(base, ".gomodcache/cache"), []byte("changed deps"), 0644)
	again, err := releaseSourceDigest(base)
	if err != nil || again != got {
		t.Fatal("cache entered source digest", err)
	}
	os.Symlink("/etc/passwd", filepath.Join(base, "cmd/alias.go"))
	if _, err := releaseSourceDigest(base); err == nil {
		t.Fatal("symlink source accepted")
	}
}

func TestLocalReleaseSealsBothImagesAndEnvironment(t *testing.T) {
	workspace, scratch := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(workspace, moduleDepsDir), 0755)
	cache := filepath.Join(workspace, moduleDepsDir, "module.go")
	os.WriteFile(cache, []byte("dependency"), 0644)
	for name, raw := range map[string]string{"controller": strings.Repeat("c", 100), "broker": strings.Repeat("b", 100), "toolchain.txt": "go version go1.26 linux/arm64\n"} {
		os.WriteFile(filepath.Join(scratch, name), []byte(raw), 0600)
	}
	m := Manifest{Purpose: LocalReleasePurpose, Pin: Pin{PermitID: strings.Repeat("a", 32)}, Source: &SourceSnapshot{Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40), ReleaseDigest: strings.Repeat("d", 64)}}
	collect := func() releaseprep.LocalReceipt {
		dest := filepath.Join(t.TempDir(), "artifacts")
		if err := collectLocalRelease(m, scratch, workspace, dest, uint32(os.Geteuid())); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dest, "receipt.json"))
		if err != nil {
			t.Fatal(err)
		}
		var r releaseprep.LocalReceipt
		if json.Unmarshal(raw, &r) != nil {
			t.Fatal("receipt unreadable")
		}
		for _, name := range []string{"controller", "broker"} {
			info, err := os.Stat(filepath.Join(dest, name))
			if err != nil || info.Mode().Perm() != 0400 {
				t.Fatal("artifact not sealed")
			}
		}
		return r
	}
	first := collect()
	if first.BinarySHA == first.BrokerSHA || first.SourceDigest != m.Source.ReleaseDigest || first.BuildPermit != m.Pin.PermitID {
		t.Fatal("pair/source not bound")
	}
	os.WriteFile(cache, []byte("changed dependency"), 0644)
	second := collect()
	if first.BuildEnvDigest == second.BuildEnvDigest {
		t.Fatal("module cache not bound")
	}
	os.WriteFile(filepath.Join(scratch, "toolchain.txt"), []byte("go version go1.27 linux/arm64\n"), 0600)
	if collect().BuildEnvDigest == second.BuildEnvDigest {
		t.Fatal("Go version not bound")
	}
	os.Remove(filepath.Join(scratch, "broker"))
	os.Symlink(filepath.Join(scratch, "controller"), filepath.Join(scratch, "broker"))
	if collectLocalRelease(m, scratch, workspace, filepath.Join(t.TempDir(), "bad"), uint32(os.Geteuid())) == nil {
		t.Fatal("symlink artifact accepted")
	}
}
