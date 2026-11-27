package haconfig

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
)

// Row binds one entity to its device, integration, area and export targets.
type Row struct {
	Entity      string   `json:"entity_id"`
	Name        string   `json:"name"`
	Platform    string   `json:"integration"`
	Device      string   `json:"device,omitempty"`
	DeviceID    string   `json:"device_id,omitempty"`
	Model       string   `json:"model,omitempty"`
	Area        string   `json:"area,omitempty"`
	AreaID      string   `json:"area_id,omitempty"`
	DeviceClass string   `json:"device_class,omitempty"`
	Category    string   `json:"entity_category,omitempty"` // config/diagnostic
	Zigbee      bool     `json:"zigbee2mqtt,omitempty"`
	Disabled    bool     `json:"disabled,omitempty"`
	Hidden      bool     `json:"hidden,omitempty"`
	Exports     []string `json:"exports"` // export entry titles
}

// Export is one export integration entry and the entities it holds.
type Export struct {
	EntryID  string   `json:"entry_id"`
	Domain   string   `json:"domain"`
	Title    string   `json:"title"`
	Entities []string `json:"entities"`
	Error    string   `json:"error,omitempty"`
}

type Inventory struct {
	Rows    []Row    `json:"rows"`
	Exports []Export `json:"exports"`
}

type configEntry struct {
	EntryID string `json:"entry_id"`
	Domain  string `json:"domain"`
	Title   string `json:"title"`
	State   string `json:"state"`
}

func ConfigEntries(ctx context.Context, api API) ([]configEntry, error) {
	var out []configEntry
	return out, api.REST(ctx, "GET", "/api/config/config_entries/entry", nil, &out)
}

// Exports reads every loaded export integration (HomeKit bridges, Yaha).
func Exports(ctx context.Context, api API) ([]Export, error) {
	entries, err := ConfigEntries(ctx, api)
	if err != nil {
		return nil, err
	}
	var out []Export
	for _, e := range entries {
		r, ok := Routes[e.Domain]
		if !ok || e.State != "loaded" {
			continue
		}
		ex := Export{EntryID: e.EntryID, Domain: e.Domain, Title: e.Title, Entities: []string{}}
		if list, err := ReadExport(ctx, api, e.EntryID, r); err != nil {
			ex.Error = err.Error()
		} else {
			ex.Entities = uniqueSorted(list)
		}
		out = append(out, ex)
	}
	return out, nil
}

// Load reads registries over WebSocket and joins them with export lists.
func Load(ctx context.Context, api API) (Inventory, error) {
	res, err := api.WS(ctx, map[string]any{"type": "config/entity_registry/list"}, map[string]any{"type": "config/device_registry/list"}, map[string]any{"type": "config/area_registry/list"})
	if err != nil {
		return Inventory{}, err
	}
	var ents []struct {
		EntityID     string  `json:"entity_id"`
		Name         *string `json:"name"`
		OriginalName *string `json:"original_name"`
		Platform     string  `json:"platform"`
		DeviceID     *string `json:"device_id"`
		AreaID       *string `json:"area_id"`
		DeviceClass  *string `json:"device_class"`
		OrigClass    *string `json:"original_device_class"`
		Category     *string `json:"entity_category"`
		DisabledBy   *string `json:"disabled_by"`
		HiddenBy     *string `json:"hidden_by"`
	}
	var devs []struct {
		ID          string     `json:"id"`
		Name        *string    `json:"name"`
		NameByUser  *string    `json:"name_by_user"`
		Model       *string    `json:"model"`
		AreaID      *string    `json:"area_id"`
		Identifiers [][]string `json:"identifiers"`
	}
	var areas []struct {
		AreaID string `json:"area_id"`
		Name   string `json:"name"`
	}
	if json.Unmarshal(res[0], &ents) != nil || json.Unmarshal(res[1], &devs) != nil || json.Unmarshal(res[2], &areas) != nil {
		return Inventory{}, errors.New("unexpected registry format")
	}
	areaName := map[string]string{}
	for _, a := range areas {
		areaName[a.AreaID] = a.Name
	}
	type dev struct {
		name, model, area string
		zigbee            bool
	}
	devByID := map[string]dev{}
	for _, d := range devs {
		v := dev{name: str(d.NameByUser), model: str(d.Model), area: str(d.AreaID)}
		if v.name == "" {
			v.name = str(d.Name)
		}
		for _, id := range d.Identifiers {
			// The coordinator itself (zigbee2mqtt_bridge_*) is not a device:
			// its permit_join switch and log sensors must never be exported.
			if len(id) == 2 && id[0] == "mqtt" && strings.HasPrefix(id[1], "zigbee2mqtt_") && !strings.HasPrefix(id[1], "zigbee2mqtt_bridge_") {
				v.zigbee = true
			}
		}
		devByID[d.ID] = v
	}
	exports, err := Exports(ctx, api)
	if err != nil {
		return Inventory{}, err
	}
	in := Inventory{Exports: exports}
	for _, e := range ents {
		r := Row{Entity: e.EntityID, Name: str(e.Name), Platform: e.Platform, DeviceID: str(e.DeviceID), AreaID: str(e.AreaID), DeviceClass: str(e.DeviceClass), Category: str(e.Category), Disabled: e.DisabledBy != nil, Hidden: e.HiddenBy != nil, Exports: []string{}}
		if r.Name == "" {
			r.Name = str(e.OriginalName)
		}
		if r.DeviceClass == "" {
			r.DeviceClass = str(e.OrigClass)
		}
		if d, ok := devByID[r.DeviceID]; ok {
			r.Device, r.Model, r.Zigbee = d.name, d.model, d.zigbee
			if r.AreaID == "" {
				r.AreaID = d.area // entities inherit the device's area
			}
		}
		r.Area = areaName[r.AreaID]
		for _, ex := range exports {
			if slices.Contains(ex.Entities, r.Entity) {
				r.Exports = append(r.Exports, ex.Title)
			}
		}
		in.Rows = append(in.Rows, r)
	}
	sort.Slice(in.Rows, func(i, j int) bool { return in.Rows[i].Entity < in.Rows[j].Entity })
	return in, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
