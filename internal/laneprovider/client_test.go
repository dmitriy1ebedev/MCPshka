package laneprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fakeCaller(responses map[string]json.RawMessage, calls *[]string) caller {
	return func(ctx context.Context, operation string, params map[string]any) (json.RawMessage, error) {
		*calls = append(*calls, operation)
		if raw, ok := responses[operation]; ok {
			return raw, nil
		}
		return nil, errors.New("unexpected operation")
	}
}

func TestPreviewAndStageParsing(t *testing.T) {
	var calls []string
	s := newService(fakeCaller(map[string]json.RawMessage{
		"plan.create": json.RawMessage(`{"ok":true,"dry_run":true,"mutations":false,"actual":{"state":"ABSENT"},"changes":["arm rollback timer"]}`),
	}, &calls))
	out, err := s.Preview(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK || !out.DryRun || out.Mutations || out.Actual.State != "ABSENT" {
		t.Fatalf("unexpected preview: %+v", out)
	}
	if !reflect.DeepEqual(calls, []string{"plan.create"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestCreateApplyCommitRollback(t *testing.T) {
	var calls []string
	n := 0
	s := newService(func(ctx context.Context, operation string, params map[string]any) (json.RawMessage, error) {
		calls = append(calls, operation)
		switch operation {
		case "plan.create":
			return json.RawMessage(`{"ok":true,"plan":{"plan_id":"plan-20261007T000000Z-abcd","created_at":"x","expires_at":"y"}}`), nil
		case "canary.action":
			n++
			switch params["action"] {
			case "apply":
				return json.RawMessage(`{"ok":true,"tx_id":"run-20261007T000000Z-abcd","plan_id":"plan-20261007T000000Z-abcd","status":"PENDING_COMMIT","rollback_armed":true,"rollback_seconds":300}`), nil
			case "commit":
				return json.RawMessage(`{"ok":true,"tx_id":"run-20261007T000000Z-abcd","status":"COMMITTED","rollback_armed":false}`), nil
			case "rollback":
				return json.RawMessage(`{"ok":true,"tx_id":"run-20261007T000000Z-abcd","status":"ROLLED_BACK","rollback_armed":false}`), nil
			}
		}
		return nil, errors.New("unexpected")
	})
	plan, err := s.CreatePlan(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.Apply(context.Background(), plan.Plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "PENDING_COMMIT" || !applied.RollbackArmed || applied.RollbackSeconds != 300 {
		t.Fatalf("unexpected apply: %+v", applied)
	}
	committed, err := s.Commit(context.Background(), applied.TxID)
	if err != nil || committed.Status != "COMMITTED" {
		t.Fatalf("commit=%+v err=%v", committed, err)
	}
	rolled, err := s.Rollback(context.Background(), applied.TxID)
	if err != nil || rolled.Status != "ROLLED_BACK" {
		t.Fatalf("rollback=%+v err=%v", rolled, err)
	}
	if n != 3 {
		t.Fatalf("canary calls=%d", n)
	}
}

func TestStatusCompactsLaneState(t *testing.T) {
	var calls []string
	s := newService(fakeCaller(map[string]json.RawMessage{
		"canary.action": json.RawMessage(`{"ok":true,"state":{"active":true,"status":"PENDING_COMMIT","tx_id":"run-abc","plan_id":"plan-abc","rollback_armed":true,"rollback_seconds":300},"live":{"state":"MATCHED","errors":[]}}`),
	}, &calls))
	out, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Active || out.Status != "PENDING_COMMIT" || out.TxID != "run-abc" || !out.RollbackArmed || out.LiveState != "MATCHED" {
		t.Fatalf("unexpected status: %+v", out)
	}
}

func TestRejectsUnsafeIdentifiersAndPreviewMutation(t *testing.T) {
	var calls []string
	s := newService(fakeCaller(map[string]json.RawMessage{
		"plan.create": json.RawMessage(`{"ok":true,"dry_run":true,"mutations":true}`),
	}, &calls))
	if _, err := s.Preview(context.Background(), ""); err == nil {
		t.Fatal("mutating preview accepted")
	}
	if _, err := s.Apply(context.Background(), "../plan"); err == nil {
		t.Fatal("unsafe plan id accepted")
	}
	if _, err := s.Commit(context.Background(), "bad"); err == nil {
		t.Fatal("unsafe tx id accepted")
	}
}

func TestReadBoundedLineRejectsOverflow(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("abcdef\n"))
	if _, err := readBoundedLine(r, 4); err == nil {
		t.Fatal("overflow accepted")
	}
}
