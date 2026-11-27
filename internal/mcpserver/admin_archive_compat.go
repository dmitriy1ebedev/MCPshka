package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"mcpshka/internal/jobs"
	"mcpshka/internal/rootbroker"
)

type archiveCompatInput struct {
	Action    string `json:"action"`
	SessionID string `json:"session_id"`
	Scope     string `json:"scope"`
}

func isArchiveCompat(name string, in map[string]any) bool {
	command, _ := in["command"].(string)
	args, _ := in["args"].([]any)
	return name == "exec_start" && (command == "mcpshka" || command == "/usr/local/bin/mcpshka") && len(args) > 0 && args[0] == "admin-archive"
}

func archiveCompatRequest(in ExecStartInput) (archiveCompatInput, error) {
	var request archiveCompatInput
	if len(in.Args) != 2 || len(in.Args[1]) > 2048 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return request, errors.New("archive compatibility route requires typed input, retry key and no overrides")
	}
	d := json.NewDecoder(strings.NewReader(in.Args[1]))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF {
		return request, errors.New("invalid archive compatibility JSON")
	}
	switch request.Action {
	case "prepare", "execute", "status", "reconcile":
	default:
		return request, errors.New("unsupported archive compatibility action")
	}
	return request, nil
}

func adminArchiveCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	request, err := archiveCompatRequest(in)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	owner, err := adminOwner(ctx)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err := m.Policy().CheckWorkdir(in.Workdir); err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err := m.Policy().DecideCommand("mcpshka-admin-archive", nil); err != nil {
		return jobs.Snapshot{}, err
	}
	out, err := runAdminSession[rootbroker.ArchiveResult](ctx, "archive-"+request.Action, request.SessionID, owner, request.Scope)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt("mcpshka", []string{"admin-archive", request.Action, request.Scope}, in.Workdir, string(raw))
}
