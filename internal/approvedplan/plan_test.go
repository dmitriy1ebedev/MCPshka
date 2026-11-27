package approvedplan

import (
	"strings"
	"testing"
)

func TestDowngradeIsVersionBound(t *testing.T) {
	for _, tc := range []struct {
		plan  Plan
		valid bool
		env   string
	}{
		{plan: Plan{}, valid: true, env: "MCPSHKA_ALLOW_DOWNGRADE="},
		{plan: Plan{AllowDowngrade: true, TargetVersion: "0.1.0"}, valid: true, env: "MCPSHKA_ALLOW_DOWNGRADE=0.1.0"},
		{plan: Plan{AllowDowngrade: true, TargetVersion: "v2+build.3"}, valid: true, env: "MCPSHKA_ALLOW_DOWNGRADE=v2+build.3"},
		{plan: Plan{AllowDowngrade: true}, valid: false},
		{plan: Plan{AllowDowngrade: true, TargetVersion: "0.1.0\nBAD=x"}, valid: false},
		{plan: Plan{TargetVersion: "0.1.0"}, valid: false},
	} {
		if err := tc.plan.ValidateDowngrade(); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc.plan, err)
		}
		if tc.valid && !strings.Contains(strings.Join(tc.plan.Environment(), "\n"), tc.env) {
			t.Fatal("wrong downgrade environment")
		}
	}
}

func TestCallerPinsCannotBeReplacedByInspection(t *testing.T) {
	p := Plan{SHA256: "exe", InputSHAs: map[string]string{"/export": "sealed"}}
	if err := p.CheckPins("exe", map[string]string{"/export": "sealed"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range []map[string]string{{"/export": "changed"}, {}} {
		if p.CheckPins("exe", got) == nil {
			t.Fatal("swapped or missing input accepted")
		}
	}
	if p.CheckPins("changed", map[string]string{"/export": "sealed"}) == nil {
		t.Fatal("swapped executable accepted")
	}
}
