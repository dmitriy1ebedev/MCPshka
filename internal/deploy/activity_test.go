package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func TestStatusObservationPreservesAuthority(t *testing.T) {
	now := time.Date(2026, 10, 16, 17, 0, 0, 0, time.UTC)
	for _, tt := range []struct{ state, started, reason string }{
		{"accepted", now.Format(time.RFC3339Nano), ""},
		{"accepted", now.Add(-5 * time.Minute).Format(time.RFC3339Nano), "STALE_DEPLOYMENT_UNKNOWN"},
		{"running", now.Add(-time.Hour).Format(time.RFC3339Nano), "STALE_DEPLOYMENT_UNKNOWN"},
		{"UNKNOWN", now.Format(time.RFC3339Nano), "DEPLOYMENT_OUTCOME_UNKNOWN"},
		{"rollback_failed", now.Format(time.RFC3339Nano), "DEPLOYMENT_OUTCOME_UNKNOWN"},
		{"accepted", "", "DEPLOYMENT_TIME_UNKNOWN"},
		{"accepted", now.Add(time.Second).Format(time.RFC3339Nano), "DEPLOYMENT_TIME_UNKNOWN"},
		{"succeeded", "", ""}, {"failed", "", ""}, {"rolled_back", "", ""},
	} {
		t.Run(tt.state+tt.started, func(t *testing.T) {
			in := Status{DeploymentID: "1792159092900-1e327387d562", State: tt.state, StartedAt: tt.started}
			got, err := observeStatus(in, now)
			if err != nil || got.State != in.State || got.ReasonCode != tt.reason || got.ReconciliationRequired != (tt.reason != "") || got.CorrelationID != in.DeploymentID || got.NextAction == "" {
				t.Fatalf("observation=%+v err=%v", got, err)
			}
			if got.FinishedAt != "" {
				t.Fatal("observation fabricated terminal evidence")
			}
		})
	}
}

func TestPermanentRetirementHasNoUnfinishedRecovery(t *testing.T) {
	id := "1792159092900-1e327387d562"
	s := fencedStatus(Status{DeploymentID: id, State: "accepted"}, []byte("root fence"), true)
	out := Reconciliation{}
	remaining := 16384
	if err := out.add(s, &remaining); err != nil {
		t.Fatal(err)
	}
	if out.Pending || out.ReconciliationRequired || out.NonterminalCount != 0 || out.RetiredCount != 1 || out.FencedCount != 1 || s.ReconciliationRequired || s.State != "accepted" || s.FinishedAt != "" {
		t.Fatalf("retirement fabricated an outcome or left unfinished work: %+v / %+v", s, out)
	}
	active, err := observeStatus(Status{DeploymentID: "1792159092901-aaaaaaaaaaaa", State: "UNKNOWN"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := out.add(active, &remaining); err != nil {
		t.Fatal(err)
	}
	if !out.Pending || !out.ReconciliationRequired || out.NonterminalCount != 1 || out.RetiredCount != 1 {
		t.Fatalf("retirement hid active UNKNOWN: %+v", out)
	}
}

func TestReconciliationSummarySeparatesFencedHistoryFromActiveUnknown(t *testing.T) {
	now := time.Now().UTC()
	id := "1792159092900-1e327387d562"
	raw := []byte(`{"deployment_id":"1792159092900-1e327387d562","state":"accepted"}`)
	gate, helper := []byte("gate"), []byte("helper")
	fence, _ := json.Marshal(deploymentFence{Schema: "mcpshka.deployment-fence/v1", DeploymentID: id,
		AuthorizationSessionID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", ReceiptSHA256: digest(raw),
		GateSHA256: digest(gate), HelperSHA256: digest(helper), ObservedCurrentSHA256: digest([]byte("current")),
		FencedAt: now.Unix(), Reason: "retired historical unknown; never replay"})
	history, err := observeStatus(Status{DeploymentID: id, State: "accepted"}, now)
	if err != nil {
		t.Fatal(err)
	}
	history, err = withVerifiedFence(history, raw, fence, gate, helper, now)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// Rebuilding the view never changes the original nonterminal receipt.
		out := Reconciliation{Deployments: []Status{}, Trust: "UNTRUSTED"}
		remaining := 16 * 1024
		if err := out.add(history, &remaining); err != nil {
			t.Fatal(err)
		}
		if out.Pending || out.ReconciliationRequired || out.FencedCount != 1 || out.NonterminalCount != 1 ||
			len(out.Deployments) != 1 || !out.Deployments[0].ReconciliationRequired || out.Deployments[0].State != "accepted" || out.Deployments[0].FinishedAt != "" {
			t.Fatalf("historical unknown presented as active work or fabricated success: %+v", out)
		}
		active, err := observeStatus(Status{DeploymentID: "1792159092901-1e327387d563", State: "UNKNOWN"}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := out.add(active, &remaining); err != nil {
			t.Fatal(err)
		}
		if !out.Pending || !out.ReconciliationRequired || out.FencedCount != 1 || out.NonterminalCount != 2 {
			t.Fatalf("history hid an active unknown: %+v", out)
		}
	}
	if string(raw) != `{"deployment_id":"1792159092900-1e327387d562","state":"accepted"}` {
		t.Fatal("summary rewrote original receipt")
	}
}

func TestReconcileSurvivesRestartWithoutReplayingOrClearingStaleAccepted(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Never bind a unit fixture to an actual production root-owned fence ID.
	id, err := deploymentID()
	if err != nil {
		t.Fatal(err)
	}
	key := "deploys/" + id + ".json"
	raw, _ := json.Marshal(Status{DeploymentID: id, State: "accepted", StartedAt: "2026-01-01T00:00:00Z"})
	if err = db.Put(key, raw); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// A new service instance models a controller restart; the persisted receipt is authority.
		s := New(nil, filepath.Join(root, "deploys"))
		out, err := s.Reconcile()
		if err != nil || !out.Pending || !out.ReconciliationRequired || out.NonterminalCount != 1 || len(out.Deployments) != 1 || out.Deployments[0].CorrelationID != id {
			t.Fatalf("reconciliation=%+v err=%v", out, err)
		}
		pending, err := s.Pending()
		if err != nil || !pending {
			t.Fatalf("pending=%v err=%v", pending, err)
		}
		after, err := db.Get(key)
		if err != nil || string(after) != string(raw) {
			t.Fatal("read-only reconciliation changed authority")
		}
	}
	// Only an independently persisted terminal helper receipt removes the blocker.
	final, _ := json.Marshal(Status{DeploymentID: id, State: "rolled_back"})
	if err = db.Put(key, final); err != nil {
		t.Fatal(err)
	}
	out, err := New(nil, filepath.Join(root, "deploys")).Reconcile()
	if err != nil || out.Pending || out.NonterminalCount != 0 {
		t.Fatalf("terminal=%+v err=%v", out, err)
	}
	// A mismatched terminal receipt must not silently remove a blocker.
	if err = db.Put(key, []byte(`{"deployment_id":"other-id","state":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = New(nil, filepath.Join(root, "deploys")).Reconcile(); err == nil {
		t.Fatal("mismatched identity accepted")
	}
}
