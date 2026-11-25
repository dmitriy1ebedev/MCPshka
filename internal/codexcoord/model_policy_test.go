package codexcoord

import (
	"sync"
	"testing"
)

func testModelCatalog() []ModelCapability {
	return []ModelCapability{
		{ID: "quick", SupportedEfforts: []string{"low", "medium"}, DefaultEffort: "low"},
		{ID: "deep", SupportedEfforts: []string{"medium", "high", "xhigh"}, DefaultEffort: "medium"},
	}
}

func TestIndependentTaskModelSelectionsAreExactAndOffline(t *testing.T) {
	specs := []ModelPreference{
		{TaskID: "task-1", SessionID: "session_one_123", Model: "quick", Effort: "low"},
		{TaskID: "task-2", SessionID: "session_two_123", Model: "deep", Effort: "high"},
		{TaskID: "task-3", SessionID: "session_three_123", Model: "deep", Effort: "xhigh"},
	}
	var wg sync.WaitGroup
	errors := make([]error, len(specs))
	for idx, task := range specs {
		wg.Add(1)
		go func(i int, p ModelPreference) {
			defer wg.Done()
			choice, err := SelectModel(p, testModelCatalog())
			if err != nil {
				errors[i] = err
				return
			}
			if choice.Preference != p || !choice.CapabilityMatched ||
				choice.CatalogVerified || choice.DispatchEnabled {
				errors[i] = errInvalidPlan
			}
		}(idx, task)
	}
	wg.Wait()
	for i, e := range errors {
		if e != nil {
			t.Fatalf("task %d %v", i, e)
		}
	}
}

type testPlanError string

func (e testPlanError) Error() string { return string(e) }

const errInvalidPlan = testPlanError("fabricated catalog/dispatch permission")

func TestModelSelectionFailClosedWithoutFallback(t *testing.T) {
	p := ModelPreference{TaskID: "one", SessionID: "session_123", Model: "deep", Effort: "xhigh"}
	cases := []struct {
		name   string
		choice ModelPreference
		list   []ModelCapability
	}{
		{"unknown_model", ModelPreference{TaskID: "one", SessionID: "session_123", Model: "missing", Effort: "high"}, testModelCatalog()},
		{"unsupported_effort", ModelPreference{TaskID: "one", SessionID: "session_123", Model: "quick", Effort: "xhigh"}, testModelCatalog()},
		{"invalid_effort", ModelPreference{TaskID: "one", SessionID: "session_123", Model: "deep", Effort: "super"}, testModelCatalog()},
		{"hidden_model", p, []ModelCapability{{ID: "deep", SupportedEfforts: []string{"xhigh"}, DefaultEffort: "xhigh", Hidden: true}}},
		{"duplicate_model", p, []ModelCapability{{ID: "deep", SupportedEfforts: []string{"xhigh"}}, {ID: "deep", SupportedEfforts: []string{"xhigh"}}}},
		{"duplicate_effort", p, []ModelCapability{{ID: "deep", SupportedEfforts: []string{"xhigh", "xhigh"}}}},
		{"bad_default", p, []ModelCapability{{ID: "deep", SupportedEfforts: []string{"xhigh"}, DefaultEffort: "low"}}},
		{"empty_catalog", p, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := SelectModel(tc.choice, tc.list)
			if err == nil || selected.DispatchEnabled || selected.CatalogVerified {
				t.Fatalf("unsafe model fallback: %+v %v", selected, err)
			}
		})
	}
}

func TestTurnSwitchRespectsOwnerAndInstalledProtocol(t *testing.T) {
	p := ModelPreference{TaskID: "task1", SessionID: "session_one123", Model: "deep", Effort: "high"}
	choice, err := SelectModel(p, testModelCatalog())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		evidence TurnSwitchEvidence
		want     string
	}{
		{"foreign_active", TurnSwitchEvidence{SessionID: p.SessionID, Active: true}, "HOLD"},
		{"wrong_session", TurnSwitchEvidence{SessionID: "session_other123", Active: true, OwnershipVerified: true, WriterFenced: true}, "HOLD"},
		{"idle", TurnSwitchEvidence{SessionID: p.SessionID}, "next_turn_candidate"},
		{"active_no_fence", TurnSwitchEvidence{SessionID: p.SessionID, Active: true, OwnershipVerified: true}, "HOLD"},
		{"no_supported_live_update", TurnSwitchEvidence{SessionID: p.SessionID, Active: true, TurnID: "turn-123", OwnershipVerified: true, WriterFenced: true, ExplicitAuthorization: true}, "next_turn_candidate"},
		{"no_auth", TurnSwitchEvidence{SessionID: p.SessionID, Active: true, TurnID: "turn-123", OwnershipVerified: true, WriterFenced: true, LiveUpdateNegotiated: true}, "next_turn_candidate"},
		{"eligible_but_not_dispatched", TurnSwitchEvidence{SessionID: p.SessionID, Active: true, TurnID: "turn-123", OwnershipVerified: true, WriterFenced: true, LiveUpdateNegotiated: true, ExplicitAuthorization: true}, "live_update_candidate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanTurnSwitch(choice, tc.evidence)
			if plan.Action != tc.want || plan.DispatchEnabled {
				t.Fatalf("unsafe switch plan: %+v", plan)
			}
		})
	}
	choice.CapabilityMatched = false
	plan := PlanTurnSwitch(choice, TurnSwitchEvidence{SessionID: p.SessionID})
	if plan.Action != "HOLD" {
		t.Fatalf("unsupported catalog authorized: %+v", plan)
	}
}
