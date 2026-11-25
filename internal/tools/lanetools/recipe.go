// Package lanetools exposes Lane's CLI through fixed root-broker recipes.
package lanetools

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

type Input struct {
	Action         string            `json:"action" jsonschema:"status, engines status, router status|wifi|clients|history|plan|apply, device list, diag export, engines update, dns apply|commit, service mode."`
	Name           string            `json:"name,omitempty" jsonschema:"Engine or service name, never CLI arguments."`
	Mode           string            `json:"mode,omitempty" jsonschema:"service mode: auto, antidpi or vpn."`
	Tx             string            `json:"tx,omitempty" jsonschema:"dns commit: transaction returned by dns apply."`
	Since          string            `json:"since,omitempty" jsonschema:"diag export window: 30m, 24h, 7d is 168h; 0 = whole bounded tails. Default 24h."`
	Form           string            `json:"form,omitempty"`
	Changes        map[string]string `json:"changes,omitempty"`
	Confirm        string            `json:"confirm,omitempty" jsonschema:"router apply: before_sha256 from router plan."`
	SessionID      string            `json:"admin_session_id,omitempty" jsonschema:"Active Face ID session required for mutations."`
	IdempotencyKey string            `json:"idempotency_key" jsonschema:"Required retry key for lane calls; reconcile uncertain replies before retrying."`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}$`)

var window = regexp.MustCompile(`^(0|[0-9]{1,4}[smh])$`)

func recipe(in Input) (args []string, stdin []byte, mutation bool, err error) {
	bad := func() ([]string, []byte, bool, error) {
		return nil, nil, false, errors.New("LANE_RECIPE_INVALID: fixed action and matching fields required")
	}
	router := in.Action == "router plan" || in.Action == "router apply"
	if !router && (in.Form != "" || len(in.Changes) != 0 || in.Confirm != "") {
		return bad()
	}
	if in.Action != "engines update" && in.Action != "service mode" && in.Name != "" {
		return bad()
	}
	if in.Action != "service mode" && in.Mode != "" || in.Action != "dns commit" && in.Tx != "" {
		return bad()
	}
	if in.Action != "diag export" && in.Since != "" || in.Since != "" && !window.MatchString(in.Since) {
		return bad()
	}
	switch in.Action {
	case "diag export":
		since := in.Since
		if since == "" {
			since = "24h"
		}
		args = []string{"diag", "export", "--since", since}
	case "status":
		args = []string{"status"}
	case "engines status":
		args = []string{"engines", "status"}
	case "router status", "router wifi", "router clients", "router history":
		args = []string{"router", in.Action[len("router "):]}
	case "device list":
		args = []string{"device", "list"}
	case "router plan", "router apply":
		if !identifier.MatchString(in.Form) || len(in.Changes) == 0 || len(in.Changes) > 64 {
			return bad()
		}
		mutation = in.Action == "router apply"
		if mutation {
			if raw, e := hex.DecodeString(in.Confirm); e != nil || len(raw) != 32 {
				return bad()
			}
		} else if in.Confirm != "" {
			return bad()
		}
		args = []string{"router", in.Action[len("router "):]}
		stdin, err = json.Marshal(struct {
			Form    string            `json:"form"`
			Changes map[string]string `json:"changes"`
			Confirm string            `json:"confirm,omitempty"`
		}{in.Form, in.Changes, in.Confirm})
		if err != nil || len(stdin) > 16<<10 {
			return bad()
		}
	case "engines update":
		if !identifier.MatchString(in.Name) {
			return bad()
		}
		args, mutation = []string{"engines", "update", in.Name, "--apply"}, true
	case "dns apply":
		args, mutation = []string{"dns", "apply"}, true
	case "dns commit":
		if !identifier.MatchString(in.Tx) {
			return bad()
		}
		args, mutation = []string{"dns", "commit", "--tx", in.Tx}, true
	case "service mode":
		if !identifier.MatchString(in.Name) || in.Mode != "auto" && in.Mode != "antidpi" && in.Mode != "vpn" {
			return bad()
		}
		args, mutation = []string{"dpi", "mode", in.Name, in.Mode}, true
	default:
		return bad()
	}
	return
}
