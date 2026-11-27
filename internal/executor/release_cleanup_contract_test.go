package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/failure"
	"mcpshka/internal/statestore"
)

// 24.11: a module-test manifest sealed under an older contract failed
// today's run check and stopped every hourly cleanup pass (2 GB of
// workspaces). It is kept; the rest of the plan proceeds. An untrusted
// manifest still stops the destructive pass.
func TestReleaseCleanupKeepsOlderContractManifestAndContinues(t *testing.T) {
	for _, tc := range []struct {
		code    string
		wantErr bool
	}{{"MANIFEST_MODULE_RUN_INVALID", false}, {"MANIFEST_METADATA_UNTRUSTED", true}} {
		base := t.TempDir()
		db, err := statestore.Open(filepath.Join(base, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		layout := cleanupLayout{bin: filepath.Join(base, "bin"), home: base, authority: filepath.Join(base, "authority"), parts: filepath.Join(base, "parts"), installed: filepath.Join(base, "installed"), installedBroker: filepath.Join(base, "broker-installed"), uid: uint32(os.Geteuid())}
		for _, p := range []string{layout.installed, layout.installedBroker} {
			os.MkdirAll(filepath.Dir(p), 0o700)
			os.WriteFile(p, []byte("image"), 0o600)
		}
		sum := sha256.Sum256([]byte("image"))
		h := hex.EncodeToString(sum[:])
		operation := strings.Repeat("9", 32)
		commit := strings.Repeat("9", 40)
		up, _ := json.Marshal(cleanupUpgrade{Tree: commit, Version: "0.1.1", CurrentVersion: "0.1.0", Operation: operation, Image: h, Previous: h, Broker: h, PreviousBroker: h})
		os.MkdirAll(layout.bin, 0o700)
		os.WriteFile(filepath.Join(layout.bin, "mcpshka-coordinated-release-0.1.1.json"), up, 0o600)
		opDir := filepath.Join(layout.authority, "coordinated-upgrades", operation)
		os.MkdirAll(opDir, 0o700)
		for _, f := range []string{"controller.previous", "broker.previous"} {
			os.WriteFile(filepath.Join(opDir, f), []byte("image"), 0o600)
		}
		os.WriteFile(filepath.Join(opDir, "100-SUCCEEDED.json"), []byte(`{"state":"SUCCEEDED"}`), 0o600)
		old := strings.Repeat("1", 32)
		pin := Pin{PermitID: old, LeaseID: strings.Repeat("a", 32), Generation: 1, ExecutionID: strings.Repeat("b", 32)}
		raw, _ := json.Marshal(Snapshot{Pin: pin, Revision: 1, State: "FENCED"})
		if err := db.Put(key(pin), raw); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(layout.authority, 0o700)
		os.MkdirAll(layout.bin, 0o700)
		os.MkdirAll(layout.parts, 0o700)
		os.WriteFile(filepath.Join(layout.authority, old+".manifest.json"), []byte("{}"), 0o600)
		ws := filepath.Join(layout.authority, "workspaces", old, "source")
		os.MkdirAll(filepath.Dir(ws), 0o700)
		os.WriteFile(ws, []byte("sealed"), 0o600)
		layout.manifest = func(id string) (Manifest, error) {
			return Manifest{}, failure.NewHold(failure.FencedUnknown, tc.code, "executor.readManifest", "x", nil)
		}
		plan, err := New(db).planReleaseCleanup(context.Background(), layout)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v", tc.code, err)
		}
		if tc.wantErr {
			continue
		}
		visible := false
		for _, item := range plan.Items {
			visible = visible || strings.Contains(item.Reason, "older contract")
			if strings.Contains(item.Path, old) && item.Action == "delete" {
				t.Fatalf("older-contract permit data scheduled for deletion: %+v", item)
			}
		}
		if !visible {
			t.Fatal("older-contract manifest not reported in the plan")
		}
	}
}
