package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"mcpshka/internal/budget"
)

func Object(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}

type Evidence struct {
	Probe    string         `json:"probe"`
	SHA256   string         `json:"sha256,omitempty"`
	State    string         `json:"state"`
	Snapshot map[string]any `json:"snapshot,omitempty"`
}

func ProbeArguments(name, probe string, in, out map[string]any, before bool) (string, map[string]any, error) {
	args := map[string]any{}
	switch probe {
	case "git_status":
		if name == "fixes_review" && (in["action"] == "list" || in["action"] == "reject") {
			probe = "state_status"
		}
	case "git_provider_query":
		if before && name == "git_provider_change" && in["action"] == "create" {
			probe = "state_status"
		} else {
			args["provider"] = in["provider"]
			args["action"] = "get"
			args["owner"] = in["owner"]
			args["repo"] = in["repo"]
		}
	case "cloud_task_status":
		args["id"] = in["id"]
	case "diag_investigation_status":
		id, _ := in["id"].(string)
		if !before && id == "" {
			id, _ = out["id"].(string)
		}
		if id == "" {
			probe = "state_status"
		} else {
			args["id"] = id
		}
	case "rescue_status":
		if !before {
			id, _ := out["id"].(string)
			if id == "" {
				if key, ok := in["idempotency_key"].(string); ok {
					sum := sha256.Sum256([]byte(key))
					id = hex.EncodeToString(sum[:16])
				}
			}
			if id != "" {
				args["request_id"] = id
			}
		}
	case "artifact_upload_status":
		if before && name == "artifact_upload_begin" {
			probe = "state_status"
		} else {
			args["id"] = in["id"]
			if !before && name == "artifact_upload_begin" {
				args["id"] = out["id"]
			}
		}
	case "storage_auth_status":
		args["provider"] = in["provider"]
	case "docker_smoke_status":
		// The run id is derived by the broker; before it there is nothing to
		// probe by id, and the before-probe refused every start in 0.1.186.
		if before {
			probe = "state_status"
		} else {
			args["id"] = out["id"]
		}
	case "mission_status":
		if !(before && name == "mission_start") {
			args["id"] = in["id"]
			if name == "mission_start" {
				args["id"] = out["id"]
			}
		}
	case "approved_task_status":
		switch {
		case before && name == "approved_task_prepare":
			probe = "state_status"
		case name == "approved_task_prepare":
			args["id"] = out["id"]
		default:
			args["id"] = in["id"]
		}
	case "storage_transfer_status", "backup_status":
		if before {
			probe = "storage_providers"
		} else {
			args["id"] = out["id"]
		}
	case "proc_list", "ps_list", "network_stage_status", "control_path_status", "state_status":
	case "service_status":
		args["service"] = in["service"]
	case "docker_status":
		args["container"] = in["container"]
	case "root_task_status":
		args["task_id"] = in["task_id"]
	case "project_lease_admin_recovery_status", "project_lease_admin_fence_preview":
		args["path"] = in["path"]
		args["session_id"] = in["session_id"]
	case "executor_admin_stale_preview":
		args["session_id"] = in["session_id"]
	case "admin_session_status":
		args["session_id"] = in["session_id"]
		if !before {
			if id, ok := out["session_id"].(string); ok && id != "" {
				args["session_id"] = id
			}
		}
	case "fs_read":
		args["path"] = in["path"]
		args["limit"] = 1024
	case "fs_stat":
		args["path"] = in["path"]
	case "release_apply_status", "diag_release_status":
		// Without the version the before-probe refused and both installs
		// were unusable through the contract (BUG-20261023-75).
		args["version"] = in["version"]
	case "roadmap_run_status":
		args["path"] = in["path"]
	case "project_lease_status", "project_onboard_preview":
		args["path"] = in["path"]
	case "workflow_checkpoint_status":
		if before {
			probe = "state_status"
		} else {
			args["workflow_id"] = in["workflow_id"]
		}
	case "workflow_status":
		if before && name == "workflow_start" {
			probe = "state_status"
		} else {
			args["id"] = in["id"]
		}
	case "codex_intent_status":
		args["id"] = in["id"]
	case "codex_task_status":
		args["session_id"] = in["session_id"]
		if name == "codex_task_submit" {
			if key, ok := in["idempotency_key"].(string); ok {
				sum := sha256.Sum256([]byte(key))
				args["request_id"] = hex.EncodeToString(sum[:16])
			}
		}
		if id, ok := in["queue_id"].(string); ok && id != "" {
			args["queue_id"] = id
		}
		if !before {
			for _, field := range []string{"queue_id", "turn_id"} {
				if id, ok := out[field].(string); ok && id != "" {
					args[field] = id
				}
			}
		}
	case "docker_terminal_status":
		args["id"] = in["id"]
		args["container"] = in["container"]
		if !before && name == "docker_terminal_open" {
			args["id"] = out["id"]
		}
	case "state_backup_status":
		id, _ := in["backup_id"].(string)
		if id == "" && !before {
			key, ok := in["idempotency_key"].(string)
			if !ok || key == "" {
				return "", nil, errors.New("backup probe requires backup_id or retry key")
			}
			sum := sha256.Sum256([]byte(key))
			id = hex.EncodeToString(sum[:16])
		}
		if id == "" {
			probe = "state_status"
		} else {
			args["backup_id"] = id
		}
	case "job_status":
		id, _ := in["job_id"].(string)
		if id == "" && !before {
			id, _ = out["id"].(string)
			if job, ok := out["job"].(map[string]any); ok {
				id, _ = job["id"].(string)
			}
		}
		if id == "" {
			probe = "state_status"
		} else {
			args["job_id"] = id
		}
	case "approval_status":
		id, _ := out["id"].(string)
		if before || id == "" {
			probe = "state_status"
		} else {
			args["approval_id"] = id
		}
	case "traffic_route_status":
		id, _ := out["run_id"].(string)
		if before || id == "" {
			probe = "control_path_status"
		} else {
			args["run_id"] = id
		}
	case "executor_runtime_status":
		args["permit_id"] = in["permit_id"]
		args["launch_id"] = in["launch_id"]
		args["workdir"] = in["workdir"]
	case "executor_fence_status", "integration_recovery_status", "integration_worker_status", "integration_approval_status":
		args["permit_id"] = in["permit_id"]
		args["approval_id"] = in["approval_id"]
		args["workdir"] = in["workdir"]
	case "integration_status":
		args["path"] = in["path"]
		args["id"] = in["id"]
	case "project_deploy_status":
		id, _ := out["deployment_id"].(string)
		if name == "admin_session_retire_deployment" {
			args["deployment_id"] = in["deployment_id"]
		} else if before || id == "" {
			probe = "control_path_status"
		} else {
			args["deployment_id"] = id
		}
	case "memory_archive_status":
		var id string
		if !before {
			for _, field := range []string{"run", "receipt"} {
				item, _ := out[field].(map[string]any)
				if id, _ = item["run_id"].(string); id != "" {
					break
				}
			}
		}
		if id == "" {
			probe = "state_status"
		} else {
			args["source"], args["run_id"] = in["source"], id
		}
	case "admin_archive_status":
		args["session_id"] = in["session_id"]
		args["scope"] = in["scope"]
	case "project_repo_preview":
		// The handover re-walks the same target. After a move the source is
		// gone, so the after probe previews only the adopted target.
		args["admin_session_id"] = in["admin_session_id"]
		args["path"] = in["path"]
		if before && in["source"] != nil {
			args["source"] = in["source"]
		}
	case "codex_session_creation_status":
		args["admin_session_id"] = in["admin_session_id"]
		args["creation_key"] = in["creation_key"]
		if in["load_key"] != nil {
			args["load_key"] = in["load_key"]
		}
	case "issue_get":
		// Without the card id every issue_update/accept/dispatch was held
		// by «before probe failed: issue not found» (MCP-130).
		args["id"] = in["id"]
	}
	return probe, args, nil
}

func Observe(ctx context.Context, probe string, args map[string]any, handler func(context.Context, map[string]any) (any, error)) (Evidence, error) {
	if handler == nil {
		return Evidence{}, errors.New("probe unavailable")
	}
	limit := 25 * time.Second
	if probe == "executor_admin_stale_preview" {
		// The broker's stale-permit scan has a 35s budget; allow transport overhead.
		limit = 40 * time.Second
	}
	// Probes stretch with H96 load like every bounded phase (budget.Phase).
	child, cancel := context.WithTimeout(ctx, budget.Phase(limit))
	defer cancel()
	result, err := handler(child, args)
	ev := Evidence{Probe: probe, State: "observed"}
	if err != nil {
		ev.State = "unknown"
		return ev, err
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 131072 {
		return Evidence{}, errors.New("probe output budget exceeded")
	}
	sum := sha256.Sum256(raw)
	ev.SHA256 = hex.EncodeToString(sum[:])
	// Preserve only short, non-secret state fields useful for compensation. Raw
	// file bytes, messages, tokens, commands and logs never enter evidence.
	if summary, e := Object(result); e == nil {
		ev.Snapshot = map[string]any{}
		for _, key := range []string{"active_state", "sub_state", "state", "running", "health", "id", "exists", "sha256", "size_bytes", "version"} {
			switch v := summary[key].(type) {
			case bool, float64:
				ev.Snapshot[key] = v
			case string:
				if len(v) <= 128 {
					ev.Snapshot[key] = v
				}
			}
		}
	}

	return ev, nil
}
