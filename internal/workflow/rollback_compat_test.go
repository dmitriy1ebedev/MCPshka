package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

// 0.8.1 pins SHA256(JSON(plans)) after discarding unknown Run fields.
func legacyPlanFingerprint(t *testing.T, r Run) string {
	t.Helper()
	raw, err := json.Marshal(r.Plans)
	if err != nil {
		t.Fatal(err)
	}
	return digest(raw)
}

func TestControlledWorkflowCannotDispatchOn081Rollback(t *testing.T) {
	e, m, root, sha := canary(t, "")
	const id = "rollback-paused"
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal(err)
	}
	if err := e.BindMission(id, strings.Repeat("a", 32), nil); err != nil {
		t.Fatal(err)
	}
	if err := e.ControlMission(id, strings.Repeat("a", 32), "paused"); err != nil {
		t.Fatal(err)
	}
	r, err := e.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if legacyPlanFingerprint(t, r) == r.Fingerprint {
		t.Fatal("0.8.1 accepts a snapshot whose pause and deadline it cannot enforce")
	}
	// A cold supported reader must retain both the binding and the owner pause.
	cold, err := New(m).Tick(id)
	if err != nil || cold.Control != "paused" || cold.MissionID != r.MissionID || cold.LaunchClaimed {
		t.Fatal("supported restart lost the control binding", cold, err)
	}
}

func TestLegacyWorkflowStillResumesButCannotGainUnsupportedMissionControls(t *testing.T) {
	e, _, root, sha := canary(t, "")
	const id = "legacy-rollback"
	r, err := e.Start(root, "release", id, sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "schema")
	legacy["fingerprint"] = legacyPlanFingerprint(t, r)
	raw, _ = json.Marshal(legacy)
	var canonical Run
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatal(err)
	}
	canonical.State = "hold"
	raw, _ = json.Marshal(canonical)
	if err := e.db.Put(runKey(id), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get(id); err != nil {
		t.Fatal("0.8.1 workflow became unreadable after upgrade", err)
	}
	if _, err := e.Start(root, "release", id, sha, "P7"); err != nil {
		t.Fatal("same legacy start lost its receipt", err)
	}
	if err := e.BindMission(id, strings.Repeat("b", 32), nil); err == nil {
		t.Fatal("legacy fingerprint accepted controls that disappear on downgrade")
	}
	current, err := e.Get(id)
	if err != nil || current.MissionID != "" {
		t.Fatal("refused legacy bind changed the original snapshot", current, err)
	}
	current, err = e.Resume(id)
	if err != nil || !current.LaunchClaimed {
		t.Fatal("unchanged legacy workflow cannot resume", current, err)
	}
}

func TestWorkflowRejectsStrippedFutureAndLegacyControlledSchemas(t *testing.T) {
	e, _, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "schema-compatibility", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	for _, mode := range []string{"stripped", "future", "legacy-controls"} {
		t.Run(mode, func(t *testing.T) {
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "stripped":
				delete(wire, "schema")
			case "future":
				wire["schema"] = "mcpshka.workflow/v999"
			case "legacy-controls":
				delete(wire, "schema")
				wire["fingerprint"] = legacyPlanFingerprint(t, r)
				wire["mission_id"] = strings.Repeat("c", 32)
				wire["control"] = "paused"
			}
			invalid, _ := json.Marshal(wire)
			var decoded Run
			if err := decodeRun(invalid, r.ID, &decoded); err == nil {
				t.Fatal("unsupported control protocol became dispatchable", mode)
			}
		})
	}
}
