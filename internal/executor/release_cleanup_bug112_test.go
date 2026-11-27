package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Root-owned legacy plans can outlive their journals. They must protect their
// own version and rollback, without disabling retirement of proven releases.
func TestReleaseCleanupBinLayoutBUG112(t *testing.T) {
	base := t.TempDir()
	db, err := statestore.Open(filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	layout := cleanupLayout{bin: filepath.Join(base, "bin"), home: base, authority: filepath.Join(base, "authority"), parts: filepath.Join(base, "parts"), installed: filepath.Join(base, "controller"), installedBroker: filepath.Join(base, "broker"), uid: uint32(os.Geteuid()), keepVersions: 2}
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	write := func(path string, raw []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write(layout.installed, []byte("controller194"))
	write(layout.installedBroker, []byte("broker194"))
	history := cleanupHistory{Images: map[string]bool{}, Builds: map[string]string{}, Operations: map[string]string{}, Times: map[string]time.Time{}}
	plans := map[int]cleanupUpgrade{}
	files := map[int][]string{}
	for _, v := range []int{180, 187, 188, 189, 190, 191, 192, 193, 194, 195} {
		version := fmt.Sprintf("0.1.%d", v)
		previous := v - 1
		if v == 187 {
			previous = 180
		}
		u := cleanupUpgrade{Version: version, CurrentVersion: fmt.Sprintf("0.1.%d", previous), Operation: fmt.Sprintf("%032x", v), Tree: fmt.Sprintf("%040x", v), Image: hash(fmt.Sprint("controller", v)), Previous: hash(fmt.Sprint("controller", previous)), Broker: hash(fmt.Sprint("broker", v)), PreviousBroker: hash(fmt.Sprint("broker", previous))}
		plans[v] = u
		write(filepath.Join(layout.bin, "mcpshka-coordinated-release-"+version+".json"), encode(u))
		if v == 180 {
			continue
		} // No journal/history: retained as UNKNOWN.
		controller := fmt.Sprintf("mcpshka-%s.%s.candidate", version, u.Image[:12])
		broker := fmt.Sprintf("mcpshka-broker-%s.%s.candidate", version, u.Broker[:12])
		for _, name := range []string{controller, broker, controller + ".ci-receipt.json", controller + ".release-plan.json"} {
			raw := []byte("{}")
			if name == controller {
				raw = []byte(fmt.Sprint("controller", v))
			}
			if name == broker {
				raw = []byte(fmt.Sprint("broker", v))
			}
			path := filepath.Join(layout.bin, name)
			write(path, raw)
			files[v] = append(files[v], path)
		}
		if v < 190 {
			// Pre-BUG109 history had terminal operations/times, but no Releases index.
			history.Operations[u.Operation] = "SUCCEEDED"
			history.Times[u.Operation] = time.Now().Add(time.Duration(v-200) * time.Hour)
		} else {
			state := "SUCCEEDED"
			if v == 195 {
				state = "SWAPPING"
			}
			event := filepath.Join(layout.authority, "coordinated-upgrades", u.Operation, "100-"+state+".json")
			write(event, encode(map[string]string{"state": state}))
			at := time.Now().Add(time.Duration(v-200) * time.Hour)
			if err := os.Chtimes(event, at, at); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(layout.authority, "coordinated-upgrades", u.Operation, "controller.previous"), []byte(fmt.Sprint("controller", previous)))
			write(filepath.Join(layout.authority, "coordinated-upgrades", u.Operation, "broker.previous"), []byte(fmt.Sprint("broker", previous)))
		}
	}
	writeHistory := func() {
		t.Helper()
		if err := db.Put(cleanupHistoryKey, encode(history)); err != nil {
			t.Fatal(err)
		}
	}
	writeHistory()
	check := func(protectedOld bool) ReleaseCleanupPlan {
		t.Helper()
		plan, err := New(db).planReleaseCleanup(context.Background(), layout)
		if err != nil {
			t.Fatal(err)
		}
		actions := map[string]string{}
		for _, item := range plan.Items {
			actions[item.Path] = item.Action
		}
		for _, v := range []int{187, 188, 189, 190, 191, 192, 193, 194, 195} {
			want := "delete"
			if v >= 193 || protectedOld && v == 188 {
				want = "keep"
			}
			for _, path := range files[v] {
				if actions[path] != want {
					t.Errorf("%s: got %q, want %s", filepath.Base(path), actions[path], want)
				}
			}
			path := filepath.Join(layout.bin, fmt.Sprintf("mcpshka-coordinated-release-0.1.%d.json", v))
			if want == "delete" && actions[path] != "delete" {
				t.Errorf("completed plan remains: %s", path)
			}
			if want == "keep" && actions[path] == "delete" {
				t.Errorf("protected plan removed: %s", path)
			}
		}
		if actions[filepath.Join(layout.bin, "mcpshka-coordinated-release-0.1.180.json")] == "delete" {
			t.Fatal("unproven legacy release retired")
		}
		if plan.Current != "0.1.194" || plan.Rollback != "0.1.193" {
			t.Fatal(plan.Current, plan.Rollback)
		}
		return plan
	}
	plan := check(false)
	// A fresh planner must retain terminal proof from the legacy operation history.
	if err := db.Put(cleanupHistoryKey, encode(plan.history)); err != nil {
		t.Fatal(err)
	}
	check(false)
	// Pending candidates can reference an old-version filename.
	pending := plans[195]
	pending.ControllerCandidate, pending.BrokerCandidate = files[188][0], files[188][1]
	write(filepath.Join(layout.bin, "mcpshka-coordinated-release-0.1.195.json"), encode(pending))
	check(true)
	// An unfinished operation's rollback can be older than the installed rollback.
	pending = plans[195]
	pending.CurrentVersion = "0.1.188"
	pending.Previous = plans[188].Image
	pending.PreviousBroker = plans[188].Broker
	write(filepath.Join(layout.bin, "mcpshka-coordinated-release-0.1.195.json"), encode(pending))
	check(true)
}

// Hashing both owners is fail-closed for mutable or forged service candidates.
func TestReleaseCandidateRejectsUntrustedBinding(t *testing.T) {
	data := []byte("candidate")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	base := t.TempDir()
	layout := cleanupLayout{uid: uint32(os.Geteuid() + 1), candidateUID: uint32(os.Geteuid())}
	for _, tc := range []struct{ name, state string }{
		{"mcpshka-0.1.187." + digest[:12] + ".candidate", "SWAPPING"},
		{"mcpshka-0.1.187.000000000000.candidate", "SUCCEEDED"},
		{"mcpshka-broker-0.1.187." + digest[:12] + ".candidate", "SUCCEEDED"},
	} {
		path := filepath.Join(base, tc.name)
		if err := os.WriteFile(path, data, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := cleanupBinImageHash(context.Background(), path, layout, []cleanupUpgrade{{Version: "0.1.187", Image: digest, State: tc.state}}); err == nil {
			t.Fatal("untrusted candidate accepted", tc)
		}
	}
}
