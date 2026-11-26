package rootbroker

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHealthBrokerFixedReadOnlyProbe(t *testing.T) {
	raw, err := Run(context.Background(), Config{}, "health", []string{"broker"})
	var v struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(raw, &v) != nil || v.Version == "" {
		t.Fatalf("broker identity missing: %s %v", raw, err)
	}
	for _, args := range [][]string{nil, {"broker", "extra"}, {"restart"}, {"executor", "../foreign"}} {
		if _, err := Run(context.Background(), Config{}, "health", args); err == nil {
			t.Fatalf("accepted overrides: %v", args)
		}
	}
}

func TestHealthExecutorUnitLimits(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	prefix := "Id=mcpshka-executor-" + id + ".service\nActiveState=active\nExecMainStartTimestampMonotonic=1000000\nRuntimeMaxUSec="
	for _, tc := range []struct {
		raw  string
		now  uint64
		want string
	}{
		{prefix + "3min", 200000000, "ok"},
		{prefix + "3min", 242000000, "degraded"},
		{prefix + "infinity", 242000000, "unknown"},
		{prefix + "bad", 242000000, "unknown"},
		{prefix + "3min", 1, "unknown"},
		{"Id=foreign.service\nActiveState=active", 242000000, "unknown"},
		{"", 242000000, "ok"},
		{"Id=mcpshka-executor-" + id + ".service\nActiveState=failed", 242000000, "degraded"},
	} {
		r := parseExecutorUnits(tc.raw, tc.now)
		if r.State != tc.want {
			t.Errorf("%q at %d: %+v, want %s", tc.raw, tc.now, r, tc.want)
		}
	}
}
