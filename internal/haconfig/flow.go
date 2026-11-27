// Package haconfig manages Home Assistant configuration through HA's own
// APIs only: registries over WebSocket, integration options through their
// options flows. Reads walk a flow up to the entity list and always abort;
// a change submits the same flow with one list replaced, after a backup.
package haconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
)

// API is the subset of haprovider.Client used here (faked in tests).
type API interface {
	REST(ctx context.Context, method, path string, body, out any) error
	WS(ctx context.Context, commands ...map[string]any) ([]json.RawMessage, error)
}

const flowPath = "/api/config/config_entries/options/flow"

type field struct {
	Name    string `json:"name"`
	Default any    `json:"default"`
}

type flowStep struct {
	FlowID      string   `json:"flow_id"`
	Type        string   `json:"type"`
	StepID      string   `json:"step_id"`
	DataSchema  []field  `json:"data_schema"`
	MenuOptions []string `json:"menu_options"`
}

// Route says how to reach an integration's entity list: menu choices taken
// in order, and the form step holding the "entities" field.
type Route struct {
	Menus  []string
	Target string
	// Stop names form values that mean "no entity list here": the flow is
	// aborted before anything is submitted, because submitting that form
	// would save the options (a HomeKit bridge in accessory mode).
	Stop map[string]string
}

// Known export integrations and their routes (HA 2026.9).
var Routes = map[string]Route{
	"homekit":           {Target: "include", Stop: map[string]string{"mode": "accessory"}},
	"yandex_smart_home": {Menus: []string{"expose_settings"}, Target: "include_entities"},
}

func defaults(s flowStep) map[string]any {
	v := map[string]any{}
	for _, f := range s.DataSchema {
		if f.Default != nil {
			v[f.Name] = f.Default
		}
	}
	return v
}

func entityList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// walk drives the flow to the target step. With change nil it aborts there
// (read-only); otherwise it submits the target with entities = change(old)
// and every other value unchanged. It returns the list found before.
func walk(ctx context.Context, api API, entry string, r Route, change func([]string) []string) ([]string, error) {
	var s flowStep
	if err := api.REST(ctx, "POST", flowPath, map[string]string{"handler": entry}, &s); err != nil {
		return nil, err
	}
	submitted := false
	defer func() {
		if !submitted && s.FlowID != "" && (s.Type == "form" || s.Type == "menu") {
			_ = api.REST(context.WithoutCancel(ctx), "DELETE", flowPath+"/"+s.FlowID, nil, nil)
		}
	}()
	menus := slices.Clone(r.Menus)
	for range 8 {
		switch {
		case s.Type == "menu":
			if len(menus) == 0 || !slices.Contains(s.MenuOptions, menus[0]) {
				return nil, fmt.Errorf("unexpected menu %q in %s options", s.StepID, entry)
			}
			next := menus[0]
			menus = menus[1:]
			if err := api.REST(ctx, "POST", flowPath+"/"+s.FlowID, map[string]string{"next_step_id": next}, &s); err != nil {
				return nil, err
			}
		case s.Type == "form" && s.StepID == r.Target:
			vals := defaults(s)
			old := entityList(vals["entities"])
			if change == nil {
				return old, nil
			}
			vals["entities"] = change(slices.Clone(old))
			var done flowStep
			submitted = true
			if err := api.REST(ctx, "POST", flowPath+"/"+s.FlowID, vals, &done); err != nil {
				return old, err
			}
			// HomeKit bridge options have a second, optional form for bridged
			// device triggers after the entity allowlist. Preserve its existing
			// defaults; changing which entities are visible must not alter triggers.
			if r.Target == "include" && done.Type == "form" && done.StepID == "bridged_device_triggers" {
				var finish flowStep
				if err := api.REST(ctx, "POST", flowPath+"/"+done.FlowID, defaults(done), &finish); err != nil {
					return old, err
				}
				done = finish
			}
			if done.Type != "create_entry" {
				return old, fmt.Errorf("%s options did not save (step %q)", entry, done.StepID)
			}
			return old, nil
		case s.Type == "form" && stopped(s, r.Stop):
			return nil, fmt.Errorf("%s has no entity list (single accessory)", entry)
		case s.Type == "form" && len(s.DataSchema) == 0:
			// A form with nothing to fill is a notice (a HomeKit bridge set up
			// in YAML: step "yaml"); submitting it saves the options entry.
			return nil, fmt.Errorf("%s options are managed outside the UI (step %q), nothing read", entry, s.StepID)
		case s.Type == "form":
			// Intermediate form: keep every current value.
			if err := api.REST(ctx, "POST", flowPath+"/"+s.FlowID, defaults(s), &s); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%s options ended before %q (type %q)", entry, r.Target, s.Type)
		}
	}
	return nil, errors.New("options flow too long")
}

// ReadExport returns the entity list an export integration holds; the flow is
// aborted, nothing is saved.
func ReadExport(ctx context.Context, api API, entry string, r Route) ([]string, error) {
	return walk(ctx, api, entry, r, nil)
}

// SetExport replaces the list with want (sorted, unique) and returns the old one.
func SetExport(ctx context.Context, api API, entry string, r Route, want []string) ([]string, error) {
	want = uniqueSorted(want)
	return walk(ctx, api, entry, r, func([]string) []string { return want })
}

func uniqueSorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return slices.Compact(out)
}

func stopped(s flowStep, stop map[string]string) bool {
	vals := defaults(s)
	for k, v := range stop {
		if fmt.Sprint(vals[k]) == v {
			return true
		}
	}
	return false
}
