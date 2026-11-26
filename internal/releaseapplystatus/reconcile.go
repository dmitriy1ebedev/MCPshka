package releaseapplystatus

import (
	"encoding/json"
	"mcpshka/internal/notify"
	"mcpshka/internal/releasequeue"
)

type Output struct {
	notify.Progress
	Version        string              `json:"version"`
	Unit           string              `json:"unit"`
	UnitState      string              `json:"unit_state" jsonschema:"systemd ActiveState/Result of the apply unit; inactive/unknown once collected."`
	Installed      string              `json:"installed_version"`
	State          string              `json:"state" jsonschema:"INSTALLED, APPLYING, FAILED, HOLD or NOT_STARTED."`
	Reason         string              `json:"reason,omitempty"`
	LastPhase      string              `json:"last_phase,omitempty"`
	Error          string              `json:"error,omitempty"`
	Traceback      string              `json:"traceback,omitempty"`
	RollbackErrors []map[string]string `json:"rollback_errors,omitempty"`
	// Queue is the autorelease of this version, when one was queued.
	Queue *releasequeue.View `json:"queue,omitempty"`
	// Stages never collapse into one "done": a release is not acceptance.
	Stages releasequeue.Stages `json:"stages"`
}

func Reconcile(out *Output, raw []byte, err error) {
	var root struct {
		State          string              `json:"state"`
		Reason         string              `json:"reason"`
		LastPhase      string              `json:"last_phase"`
		Error          string              `json:"error"`
		Traceback      string              `json:"traceback"`
		RollbackErrors []map[string]string `json:"rollback_errors"`
	}
	if err != nil || json.Unmarshal(raw, &root) != nil {
		out.State, out.Reason = "HOLD", "root audit unavailable; release outcome is unverified"
	} else {
		out.LastPhase, out.Error, out.Traceback, out.RollbackErrors = root.LastPhase, root.Error, root.Traceback, root.RollbackErrors
		switch root.State {
		case "SUCCEEDED":
			if out.Installed == out.Version {
				out.State = "INSTALLED"
			} else {
				out.State, out.Reason = "HOLD", "root succeeded but running version differs"
			}
		case "APPLYING":
			out.State = "APPLYING"
		case "ROLLED_BACK":
			out.State, out.Reason = "FAILED", root.Error
		case "NOT_STARTED":
			if out.Installed == out.Version {
				out.State, out.Reason = "HOLD", "installed version has no root apply evidence"
			}
		default:
			out.State, out.Reason = "HOLD", root.Reason
			if out.Reason == "" {
				out.Reason = "unrecognized root apply state: " + root.State
			}
		}
	}
	if out.State != "INSTALLED" {
		out.Stages.ProductionVerified = false
		if out.Queue != nil && out.Queue.Stage == "DONE" && (out.State == "HOLD" || out.State == "FAILED") {
			out.Queue.Stage, out.Queue.Detail = "HOLD", out.Reason
		}
	}
}
