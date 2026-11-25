package haconfig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// fakeHA keeps export lists per entry and serves their options flows and
// registries like HA 2026.9 does.
type fakeHA struct {
	lists    map[string][]string
	failSave map[string]bool
	flows    map[string]string // flow id → entry
	step     map[string]string // flow id → current step
	aborted  int
	saves    int
}

func newFake(t *testing.T) *fakeHA {
	BackupDir = t.TempDir()
	return &fakeHA{
		lists: map[string][]string{"hk": {"light.old"}, "ya": {"light.old", "sensor.t"}},
		flows: map[string]string{}, step: map[string]string{}, failSave: map[string]bool{},
	}
}

func j(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (f *fakeHA) REST(ctx context.Context, method, path string, body, out any) error {
	set := func(v any) { raw, _ := json.Marshal(v); json.Unmarshal(raw, out) }
	switch {
	case method == "GET" && path == "/api/config/config_entries/entry":
		set([]map[string]string{{"entry_id": "hk", "domain": "homekit", "title": "HASS Bridge", "state": "loaded"}, {"entry_id": "ya", "domain": "yandex_smart_home", "title": "Yaha Cloud", "state": "loaded"}, {"entry_id": "mq", "domain": "mqtt", "title": "mqtt", "state": "loaded"}, {"entry_id": "acc", "domain": "homekit", "title": "LG TV", "state": "loaded"}, {"entry_id": "yml", "domain": "homekit", "title": "LG TV HA", "state": "loaded"}})
	case method == "POST" && path == flowPath:
		entry := body.(map[string]string)["handler"]
		id := "f" + entry
		f.flows[id] = entry
		if entry == "yml" {
			// A bridge from YAML: the options flow is a bare notice.
			f.step[id] = "yaml"
			set(map[string]any{"flow_id": id, "type": "form", "step_id": "yaml", "data_schema": []any{}})
		} else if entry == "ya" {
			f.step[id] = "menu"
			set(map[string]any{"flow_id": id, "type": "menu", "step_id": "init", "menu_options": []string{"expose_settings", "maintenance"}})
		} else {
			mode := "bridge"
			if entry == "acc" {
				mode = "accessory"
			}
			f.step[id] = "init"
			set(map[string]any{"flow_id": id, "type": "form", "step_id": "init", "data_schema": []map[string]any{{"name": "mode", "default": mode}}})
		}
	case method == "DELETE" && strings.HasPrefix(path, flowPath+"/"):
		f.aborted++
	case method == "POST" && strings.HasPrefix(path, flowPath+"/"):
		id := strings.TrimPrefix(path, flowPath+"/")
		entry := f.flows[id]
		target := map[string]string{"hk": "include", "ya": "include_entities"}[entry]
		switch f.step[id] {
		case "yaml":
			f.saves++ // HA saves the options entry and reloads the bridge
			set(map[string]any{"type": "create_entry"})
		case "menu":
			f.step[id] = "expose_settings"
			set(map[string]any{"flow_id": id, "type": "form", "step_id": "expose_settings", "data_schema": []map[string]any{{"name": "filter_source", "default": "config_entry"}}})
		case "init", "expose_settings":
			if entry == "acc" {
				f.saves++ // submitting an accessory bridge's form saves it
				set(map[string]any{"type": "create_entry"})
				return nil
			}
			f.step[id] = target
			set(map[string]any{"flow_id": id, "type": "form", "step_id": target, "data_schema": []map[string]any{{"name": "entities", "default": f.lists[entry]}}})
		case target:
			if f.failSave[entry] {
				return errors.New("ha down")
			}
			vals := body.(map[string]any)
			f.lists[entry] = vals["entities"].([]string)
			if entry == "hk" {
				// HomeKit 2026.9 requires the follow-up device-trigger form
				// before options are actually saved.
				f.step[id] = "bridged_device_triggers"
				set(map[string]any{"flow_id": id, "type": "form", "step_id": "bridged_device_triggers", "data_schema": []map[string]any{{"name": "existing_triggers", "default": []string{"doorbell"}}}})
				return nil
			}
			f.saves++
			set(map[string]any{"type": "create_entry"})
		case "bridged_device_triggers":
			vals := body.(map[string]any)
			if !slices.Equal(entityList(vals["existing_triggers"]), []string{"doorbell"}) {
				return errors.New("HomeKit device triggers must be preserved")
			}
			f.saves++
			set(map[string]any{"type": "create_entry"})
		}
	}
	return nil
}

func (f *fakeHA) WS(ctx context.Context, cmds ...map[string]any) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, c := range cmds {
		switch c["type"] {
		case "config/entity_registry/list":
			out = append(out, j([]map[string]any{
				{"entity_id": "light.old", "platform": "mqtt", "device_id": "d1"},
				{"entity_id": "light.new", "platform": "mqtt", "device_id": "d2", "original_name": "Bulb"},
				{"entity_id": "switch.wall", "platform": "mqtt", "device_id": "d3"},
				{"entity_id": "sensor.t", "platform": "mqtt", "device_id": "d1", "original_device_class": "temperature"},
				{"entity_id": "sensor.lqi", "platform": "mqtt", "device_id": "d2", "entity_category": "diagnostic"},
				{"entity_id": "update.fw", "platform": "mqtt", "device_id": "d2", "entity_category": "config"},
				{"entity_id": "light.tuya", "platform": "tuya", "device_id": "d9"},
				{"entity_id": "switch.zigbee2mqtt_bridge_permit_join", "platform": "mqtt", "device_id": "d8"},
			}))
		case "config/device_registry/list":
			z := [][]string{{"mqtt", "zigbee2mqtt_0x1"}}
			out = append(out, j([]map[string]any{{"id": "d1", "name": "Old", "identifiers": z, "area_id": "bed"}, {"id": "d2", "name": "Bulb", "identifiers": z, "area_id": "bed"}, {"id": "d3", "name": "Wall", "identifiers": z, "area_id": "bed"}, {"id": "d9", "name": "Tuya", "identifiers": [][]string{{"tuya", "x"}}}, {"id": "d8", "name": "Zigbee2MQTT Bridge", "model": "Bridge", "identifiers": [][]string{{"mqtt", "zigbee2mqtt_bridge_0x9"}}, "area_id": "bed"}}))
		case "config/area_registry/list":
			out = append(out, j([]map[string]any{{"area_id": "bed", "name": "Спальня"}}))
		}
	}
	return out, nil
}

func TestInventoryJoinsDevicesAreasAndExports(t *testing.T) {
	f := newFake(t)
	in, err := Load(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	var old Row
	for _, r := range in.Rows {
		if r.Entity == "light.old" {
			old = r
		}
	}
	if old.Area != "Спальня" || !old.Zigbee || len(old.Exports) != 2 {
		t.Fatalf("%+v", old)
	}
	for _, ex := range in.Exports {
		if ex.EntryID == "yml" && !strings.Contains(ex.Error, `step "yaml"`) {
			t.Fatalf("YAML bridge must be reported, not read: %+v", ex)
		}
	}
	if f.saves != 0 || f.aborted != 4 {
		t.Fatalf("reads must abort flows without saving: saves %d aborted %d", f.saves, f.aborted)
	}
}

func TestPlanApplyIsIncrementalAndIdempotent(t *testing.T) {
	f := newFake(t)
	in, _ := Load(t.Context(), f)
	p, err := Plan(in, nil, nil)
	if err != nil || len(p.Targets) != 2 {
		t.Fatal(p, err)
	}
	for _, tp := range p.Targets {
		for _, e := range tp.Add {
			if e == "sensor.lqi" || e == "update.fw" || e == "light.tuya" || e == "light.old" || strings.Contains(e, "bridge") {
				t.Fatalf("%s must not be planned for %s", e, tp.Title)
			}
		}
	}
	if len(p.Warnings) != 3 {
		t.Fatalf("accessory, YAML bridge and switch+light room not flagged: %v", p.Warnings)
	}
	res, err := Apply(t.Context(), f, p.Digest, nil, nil)
	if err != nil || len(res.Verified) != 2 || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Contains(f.lists["hk"], "light.new") || !slices.Contains(f.lists["hk"], "light.old") || !slices.Contains(f.lists["hk"], "switch.wall") {
		t.Fatalf("hk %v", f.lists["hk"])
	}
	in2, _ := Load(t.Context(), f)
	p2, _ := Plan(in2, nil, nil)
	for _, tp := range p2.Targets {
		if len(tp.Add) != 0 {
			t.Fatalf("second run adds %v", tp.Add)
		}
	}
	if _, err := Apply(t.Context(), f, p.Digest, nil, nil); err == nil {
		t.Fatal("stale plan applied")
	}
}

func TestApplyRollsBackOnFailure(t *testing.T) {
	f := newFake(t)
	in, _ := Load(t.Context(), f)
	p, _ := Plan(in, nil, nil)
	f.failSave["ya"] = true
	res, err := Apply(t.Context(), f, p.Digest, nil, nil)
	if err == nil {
		t.Fatal("partial failure not reported")
	}
	if !slices.Equal(f.lists["hk"], []string{"light.old"}) {
		t.Fatalf("homekit not restored after Yaha failed: %v (%+v)", f.lists["hk"], res)
	}
	f.failSave["ya"] = false
	if _, err := Restore(t.Context(), f, res.Backup); err != nil {
		t.Fatal(err)
	}
}

func TestPlanAddsExactlyTheChosenEntitiesAndReportsStale(t *testing.T) {
	f := newFake(t)
	f.lists["ya"] = append(f.lists["ya"], "switch.gone")
	in, _ := Load(t.Context(), f)
	p, err := Plan(in, []string{"ya"}, []string{"light.new"})
	if err != nil || len(p.Targets) != 1 || !slices.Equal(p.Targets[0].Add, []string{"light.new"}) {
		t.Fatalf("only the chosen entity must be added: %+v %v", p, err)
	}
	if !slices.Equal(p.Targets[0].Stale, []string{"switch.gone"}) {
		t.Fatalf("stale export entry not reported: %+v", p.Targets[0])
	}
	if _, err := Plan(in, nil, []string{"light.nope"}); err == nil {
		t.Fatal("unknown entity accepted")
	}
	if _, err := Apply(t.Context(), f, p.Digest, []string{"ya"}, []string{"light.new"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.lists["ya"], "light.new") || slices.Contains(f.lists["ya"], "switch.wall") {
		t.Fatalf("apply added more than chosen: %v", f.lists["ya"])
	}
}

func TestDomainTargetSkipsUnreadableBridgesButEntryTargetRefuses(t *testing.T) {
	f := newFake(t)
	in, _ := Load(t.Context(), f)
	p, err := Plan(in, []string{"homekit"}, []string{"light.new"})
	if err != nil || len(p.Targets) != 1 || p.Targets[0].EntryID != "hk" || p.Targets[0].Keep != 1 {
		t.Fatalf("domain target must plan only the readable bridge: %+v %v", p, err)
	}
	if len(p.Warnings) < 2 {
		t.Fatalf("skipped YAML/accessory bridges must be reported: %v", p.Warnings)
	}
	if _, err := Plan(in, []string{"yml"}, []string{"light.new"}); err == nil {
		t.Fatal("an explicitly named unreadable bridge must refuse")
	}
	saves := f.saves
	if _, err := Apply(t.Context(), f, p.Digest, []string{"homekit"}, []string{"light.new"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(uniqueSorted(f.lists["hk"]), []string{"light.new", "light.old"}) || !slices.Equal(f.lists["ya"], []string{"light.old", "sensor.t"}) {
		t.Fatalf("only the HomeKit bridge may change, keeping its entries: %v", f.lists)
	}
	if f.saves != saves+1 {
		t.Fatalf("YAML/accessory bridges must not be saved: %d saves", f.saves-saves)
	}
}

func TestDigestCoversExistingListsAndBackupOnlyTargets(t *testing.T) {
	f := newFake(t)
	in, _ := Load(t.Context(), f)
	p, err := Plan(in, []string{"hk"}, []string{"light.new"})
	if err != nil {
		t.Fatal(err)
	}
	f.lists["hk"] = []string{"switch.wall"} // same size, different entry
	if _, err := Apply(t.Context(), f, p.Digest, []string{"hk"}, []string{"light.new"}); err == nil {
		t.Fatal("a same-size change of the existing list must invalidate the reviewed digest")
	}
	in, _ = Load(t.Context(), f)
	p, _ = Plan(in, []string{"hk"}, []string{"light.new"})
	res, err := Apply(t.Context(), f, p.Digest, []string{"hk"}, []string{"light.new"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(res.Backup)
	var b Backup
	if json.Unmarshal(raw, &b) != nil || len(b.Exports) != 1 || !slices.Equal(b.Exports["hk"], []string{"switch.wall"}) {
		t.Fatalf("backup must hold only the chosen bridge: %s", raw)
	}
	f.lists["ya"] = []string{"changed.by.owner"}
	if _, err := Restore(t.Context(), f, res.Backup); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.lists["ya"], []string{"changed.by.owner"}) || !slices.Equal(f.lists["hk"], []string{"switch.wall"}) {
		t.Fatalf("restore must rewrite only the chosen bridge: %v", f.lists)
	}
}
