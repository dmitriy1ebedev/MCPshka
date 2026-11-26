package approvedtask

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCallResultReadsOnlyOriginalTaskAndRedactsRawOutput(t *testing.T) {
	id := strings.Repeat("a", 32)
	calls := 0
	read := func(_ context.Context, name string, args map[string]any) (map[string]any, error) {
		calls++
		if name != "approved_task_status" || len(args) != 1 || args["id"] != id {
			t.Fatalf("unsafe lookup: %s %v", name, args)
		}
		return map[string]any{"id": id, "state": "VERIFY_FAILED", "effect": "applied", "exit_code": 0,
			"output": "private", "plan": map[string]any{"args": []string{"private"}}}, nil
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		out, err := readCallResult(ctx, id, read)
		if err != nil || out.CallID != id || out.State != "VERIFY_FAILED" || out.ExitCode == nil || *out.ExitCode != 0 {
			t.Fatalf("read original result: %+v %v", out, err)
		}
		raw, err := json.Marshal(out)
		if err != nil || strings.Contains(string(raw), "private") {
			t.Fatalf("receipt exposed raw command data: %s %v", raw, err)
		}
	}
	if _, err := readCallResult(ctx, "wrong-id", read); err == nil || calls != 2 {
		t.Fatalf("validated lookup called %d times, invalid id error %v", calls, err)
	}
}
