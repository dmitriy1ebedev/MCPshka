package codexcoord

// Model-routing decisions are pure and do not launch or control Codex.
// The caller must separately prove catalog origin, session ownership, lease,
// policy, and the installed protocol before executing a real turn.
import (
	"errors"
	"fmt"
	"slices"
)

type ModelCapability struct {
	ID               string   `json:"model"`
	SupportedEfforts []string `json:"supported_reasoning_efforts"`
	DefaultEffort    string   `json:"default_reasoning_effort"`
	Hidden           bool     `json:"hidden,omitempty"`
}

type ModelPreference struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Effort    string `json:"reasoning_effort"`
}

type ModelChoice struct {
	Preference        ModelPreference `json:"preference"`
	CapabilityMatched bool            `json:"capability_matched"`
	CatalogVerified   bool            `json:"catalog_verified"`
	DispatchEnabled   bool            `json:"dispatch_enabled"`
	NextSafeAction    string          `json:"next_safe_action"`
}

var supportedEffortNames = []string{"minimal", "low", "medium", "high", "xhigh"}

// SelectModel checks exact requested settings. No fallback, default-model
// selection, permission change, or silent effort downgrade is performed.
// The catalog is only a syntactic snapshot; its origin remains unverified.
func SelectModel(preference ModelPreference, models []ModelCapability) (ModelChoice, error) {
	result := ModelChoice{Preference: preference, NextSafeAction: "verify_live_catalog_owner_and_scoped_authorization"}
	if !validID.MatchString(preference.TaskID) || !validSession.MatchString(preference.SessionID) ||
		!validModel.MatchString(preference.Model) || !slices.Contains(supportedEffortNames, preference.Effort) {
		return result, errors.New("invalid task/session/model/effort")
	}
	if len(models) == 0 || len(models) > 100 {
		return result, errors.New("catalog must contain 1..100 entries")
	}
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		if !validModel.MatchString(m.ID) || seen[m.ID] || len(m.SupportedEfforts) > 5 {
			return result, errors.New("ambiguous or invalid model catalog")
		}
		seen[m.ID] = true
		used := map[string]bool{}
		for _, e := range m.SupportedEfforts {
			if !slices.Contains(supportedEffortNames, e) || used[e] {
				return result, errors.New("invalid or duplicate reasoning capability")
			}
			used[e] = true
		}
		if m.DefaultEffort != "" && !used[m.DefaultEffort] {
			return result, errors.New("invalid default reasoning capability")
		}
		if m.ID == preference.Model {
			if m.Hidden || !used[preference.Effort] {
				return result, fmt.Errorf("requested model or reasoning effort unavailable")
			}
			result.CapabilityMatched = true
		}
	}
	if !result.CapabilityMatched {
		return result, errors.New("requested model absent from bounded snapshot")
	}
	return result, nil
}

type TurnSwitchEvidence struct {
	SessionID             string `json:"session_id"`
	TurnID                string `json:"turn_id,omitempty"`
	Active                bool   `json:"active"`
	OwnershipVerified     bool   `json:"ownership_verified,omitempty"`
	WriterFenced          bool   `json:"writer_fenced,omitempty"`
	LiveUpdateNegotiated  bool   `json:"live_update_negotiated,omitempty"`
	ExplicitAuthorization bool   `json:"explicit_authorization,omitempty"`
}
type TurnSwitchPlan struct {
	Requested       ModelPreference `json:"requested"`
	Action          string          `json:"action"`
	Reason          string          `json:"reason"`
	DispatchEnabled bool            `json:"dispatch_enabled"`
}

// PlanTurnSwitch is advisory. Returning live_update_candidate does NOT
// authorize a mutation; the live broker must independently re-check evidence.
// If an active owner cannot be proven, the correct response is HOLD; if the
// installed daemon lacks turn/settings/update, stage next-turn settings.
func PlanTurnSwitch(choice ModelChoice, current TurnSwitchEvidence) TurnSwitchPlan {
	out := TurnSwitchPlan{Requested: choice.Preference, Action: "HOLD", DispatchEnabled: false}
	if !choice.CapabilityMatched || choice.DispatchEnabled {
		out.Reason = "untrusted or unsupported model selection"
		return out
	}
	if choice.Preference.SessionID != current.SessionID {
		out.Reason = "session identity mismatch"
		return out
	}
	if current.Active && (!current.OwnershipVerified || !current.WriterFenced) {
		out.Reason = "active session ownership or writer fence unknown"
		return out
	}
	if !current.Active {
		out.Action = "next_turn_candidate"
		out.Reason = "requires catalog provenance and owner admission before start"
		return out
	}
	if current.TurnID == "" || !current.ExplicitAuthorization || !current.LiveUpdateNegotiated {
		out.Action = "next_turn_candidate"
		out.Reason = "live turn reconfiguration not authorized or protocol not negotiated"
		return out
	}
	out.Action = "live_update_candidate"
	out.Reason = "requires independent live capability and owner authorization recheck"
	return out
}
