package hosttriage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCP180Ext4Writeback600DetectedWithoutMutation(t *testing.T) {
	raw := []byte("36 25 179:2 / / rw,relatime - ext4 /dev/mmcblk1p2 rw,data=writeback,commit=600,errors=remount-ro\n")
	fs, risks, err := parseRootMountRisk(raw)
	if err != nil || fs != "ext4" || len(risks) != 2 || risks[0] != "data=writeback" || risks[1] != "commit=600" {
		t.Fatalf("MCP-180 writeback+600 risk hidden: %q %v %v", fs, risks, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	old := rootMountInfoPath
	rootMountInfoPath = path
	t.Cleanup(func() { rootMountInfoPath = old })
	problems := rootMountRisk()
	if len(problems) != 1 || problems[0].Severity != "warning" ||
		problems[0].Kind != "filesystem_crash_consistency" || problems[0].Action != nil ||
		!strings.Contains(problems[0].Evidence, "commit=600") {
		t.Fatalf("mount warning or no-auto-remount contract failed: %+v", problems)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("read-only mount triage mutated source", err)
	}
}

func TestMCP180OrderedExt4AndOtherFSNotMisdiagnosed(t *testing.T) {
	for _, tc := range []struct{
		name, row, fs string
	}{
		{"ordered", "36 25 179:2 / / rw,relatime - ext4 /dev/mmcblk1p2 rw,data=ordered,commit=5\n", "ext4"},
		{"default", "36 25 179:2 / / rw,relatime - ext4 /dev/mmcblk1p2 rw,errors=remount-ro\n", "ext4"},
		{"overlay", "36 25 0:12 / / rw,relatime - overlay overlay rw,lowerdir=x\n", "overlay"},
	}{
		t.Run(tc.name, func(t *testing.T) {
			fs, risks, err := parseRootMountRisk([]byte(tc.row))
			if err != nil || fs != tc.fs || len(risks) != 0 {
				t.Fatalf("%s: fs=%s risk=%v err=%v", tc.name, fs, risks, err)
			}
		})
	}
}

func TestMCP180UnsafeOrAmbiguousMountEvidenceFailsClosed(t *testing.T) {
	for _, raw := range []string{
		"",
		"bad line\n",
		"36 25 179:2 / / rw,relatime - ext4 /dev/mmcblk1p2 rw,commit=oops\n",
		"36 25 179:2 / / rw - ext4 /dev/root rw,commit=600\n37 25 179:3 / / rw - ext4 /dev/root2 rw,data=ordered\n",
		"36 25 179:2 / / rw,relatime - ext4 /dev/root\n",
		"36 25 179:2 / /home rw - ext4 /dev/root rw,data=writeback\n",
	} {
		if _, _, err := parseRootMountRisk([]byte(raw)); err == nil {
			t.Fatalf("invalid root mount accepted: %q", raw)
		}
	}
	old := rootMountInfoPath
	rootMountInfoPath = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func(){rootMountInfoPath = old})
	problems := rootMountRisk()
	if len(problems) != 1 || problems[0].Kind != "filesystem_mount_evidence_unknown" ||
		problems[0].Severity != "warning" || problems[0].Action != nil {
		t.Fatalf("failed mount inspection falsely healthy: %+v", problems)
	}
}

func TestMCP180OtherJournalRiskOptionsDetected(t *testing.T) {
	raw := []byte("36 25 179:2 / / rw - ext4 /dev/root rw,commit=60,nobarrier,errors=continue\n")
	_, risks, err := parseRootMountRisk(raw)
	if err != nil || len(risks) != 3 {
		t.Fatalf("unstable journaling options missing: %v %v", risks, err)
	}
	if risks[0] != "commit=60" || risks[1] != "journal barriers disabled" || risks[2] != "errors=continue" {
		t.Fatalf("risk order/confidence invalid: %v", risks)
	}
}
