package releaseapplystatus

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/releaseapply"
)

func TestCoordinatedUpgradeOffline(t *testing.T) {
	path, err := filepath.Abs("../../packaging/native/test_coordinated_upgrade.py")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("python3", "-I", "-B", path).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestReleaseApplyStatusRootAudit(t *testing.T) {
	oldAudit, oldResult := AuditDir, releaseapply.ResultDir
	t.Cleanup(func() { AuditDir, releaseapply.ResultDir = oldAudit, oldResult })
	AuditDir, releaseapply.ResultDir = t.TempDir(), t.TempDir()
	op := strings.Repeat("a", 32)
	dir := filepath.Join(AuditDir, op)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	report := []byte(`{"version":"0.13.5","state":"APPLYING","unit":"mcpshka-release-apply-0135","operation_id":"` + op + `"}`)
	if err := releaseapply.SaveResult("0.13.5", report); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "099-SUCCEEDED.json"), []byte(`{"schema":"mcpshka.paired-upgrade-audit/v1","state":"SUCCEEDED"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"SWAPPING", "SUCCEEDED", "ROLLED_BACK", "ROLLBACK_FAILED", "UNKNOWN"} {
		raw, _ := json.Marshal(map[string]string{"schema": "mcpshka.paired-upgrade-audit/v1", "state": phase, "error": "apply exit=1", "traceback": "original traceback"})
		if err := os.WriteFile(filepath.Join(dir, "100-event.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := Root([]string{"0.13.5"})
		var got map[string]any
		if err != nil || json.Unmarshal(raw, &got) != nil {
			t.Fatal(string(raw), err)
		}
		want := map[string]string{"SWAPPING": "APPLYING", "SUCCEEDED": "SUCCEEDED", "ROLLED_BACK": "ROLLED_BACK", "ROLLBACK_FAILED": "HOLD", "UNKNOWN": "HOLD"}[phase]
		if got["state"] != want || got["last_phase"] != phase || got["error"] != "apply exit=1" || got["traceback"] != "original traceback" {
			t.Fatal(got)
		}
	}
	if _, err := Root([]string{"../../etc/passwd"}); err == nil {
		t.Fatal("accepted path")
	}
}
