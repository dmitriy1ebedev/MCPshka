package haconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// BackupDir holds export lists and entity names as they were before a change.
var BackupDir = "/var/lib/mcpshka/ha-backups"

// Device types users control, and the sensors worth exporting.
var (
	controllable  = map[string]bool{"light": true, "switch": true, "cover": true, "climate": true, "fan": true, "lock": true, "valve": true}
	usefulSensors = map[string]bool{"temperature": true, "humidity": true, "door": true, "window": true, "opening": true, "motion": true, "occupancy": true, "moisture": true, "smoke": true, "co2": true}
)

// Wanted reports whether a Zigbee2MQTT entity belongs in voice/HomeKit export:
// controllable devices and useful sensors, never config/diagnostic, disabled
// or hidden entities (firmware, settings, link quality...).
func Wanted(r Row) bool {
	if !r.Zigbee || r.Category != "" || r.Disabled || r.Hidden {
		return false
	}
	domain, _, _ := strings.Cut(r.Entity, ".")
	if controllable[domain] {
		return true
	}
	return (domain == "sensor" || domain == "binary_sensor") && usefulSensors[r.DeviceClass]
}

type TargetPlan struct {
	EntryID string   `json:"entry_id"`
	Title   string   `json:"title"`
	Domain  string   `json:"domain"`
	Add     []string `json:"add"`
	Keep    int      `json:"keep"`
	// Stale lists exported entities Home Assistant no longer has (renamed or
	// removed devices): reported, never removed automatically.
	Stale []string `json:"stale,omitempty"`
}

type SyncPlan struct {
	Targets  []TargetPlan `json:"targets"`
	Warnings []string     `json:"warnings,omitempty"`
	Digest   string       `json:"digest"`
}

// Plan adds wanted Zigbee entities missing from each target; existing
// entries are kept untouched, so a second run adds nothing (no duplicates).
// Plan adds missing entities to each target: by default every Wanted
// Zigbee entity; with only set, exactly those entities (the owner's choice,
// they must exist in Home Assistant) and nothing else.
func Plan(in Inventory, targets, only []string) (SyncPlan, error) {
	var p SyncPlan
	known := map[string]bool{}
	for _, r := range in.Rows {
		known[r.Entity] = true
	}
	for _, e := range only {
		if !known[e] {
			return p, fmt.Errorf("%s is not an entity of this Home Assistant", e)
		}
	}
	for _, ex := range in.Exports {
		if len(targets) > 0 && !slices.Contains(targets, ex.EntryID) && !slices.Contains(targets, ex.Domain) {
			continue
		}
		if ex.Error != "" {
			// Only an entry named by its id must be readable; a domain target
			// (homekit) skips YAML-managed or single-accessory bridges of that
			// domain instead of refusing the whole plan.
			if !slices.Contains(targets, ex.EntryID) {
				p.Warnings = append(p.Warnings, ex.Title+": skipped — "+ex.Error)
				continue
			}
			return p, fmt.Errorf("cannot read %s: %s", ex.Title, ex.Error)
		}
		tp := TargetPlan{EntryID: ex.EntryID, Title: ex.Title, Domain: ex.Domain, Add: []string{}, Keep: len(ex.Entities)}
		for _, r := range in.Rows {
			pick := Wanted(r)
			if len(only) > 0 {
				pick = slices.Contains(only, r.Entity)
			}
			if pick && !slices.Contains(ex.Entities, r.Entity) {
				tp.Add = append(tp.Add, r.Entity)
			}
		}
		for _, e := range ex.Entities {
			if !known[e] {
				tp.Stale = append(tp.Stale, e)
			}
		}
		p.Targets = append(p.Targets, tp)
	}
	// A smart bulb behind a wall switch: switching the switch off makes the
	// light unavailable, so flag rooms with both.
	rooms := map[string][2]bool{}
	for _, r := range in.Rows {
		if !Wanted(r) || r.Area == "" {
			continue
		}
		v := rooms[r.Area]
		v[0] = v[0] || strings.HasPrefix(r.Entity, "light.")
		v[1] = v[1] || strings.HasPrefix(r.Entity, "switch.")
		rooms[r.Area] = v
	}
	for room, v := range rooms {
		if v[0] && v[1] {
			p.Warnings = append(p.Warnings, room+": has both a Zigbee switch and a light — check the switch does not cut the light's power")
		}
	}
	slices.Sort(p.Warnings)
	// The digest also covers each target's current list: a same-size swap
	// of existing entries after review makes a stale plan refuse.
	existing := map[string][]string{}
	for _, ex := range in.Exports {
		for _, t := range p.Targets {
			if t.EntryID == ex.EntryID {
				existing[ex.EntryID] = uniqueSorted(ex.Entities)
			}
		}
	}
	raw, _ := json.Marshal(struct {
		Targets  []TargetPlan        `json:"targets"`
		Existing map[string][]string `json:"existing"`
	}{p.Targets, existing})
	p.Digest = digest(raw)
	return p, nil
}

type Backup struct {
	At      time.Time           `json:"at"`
	Exports map[string][]string `json:"exports"`
	Path    string              `json:"path"`
}

func saveBackup(b Backup) (string, error) {
	if err := os.MkdirAll(BackupDir, 0o700); err != nil {
		return "", err
	}
	b.Path = filepath.Join(BackupDir, "exports-"+b.At.Format("20060102-150405.000")+".json")
	raw, _ := json.MarshalIndent(b, "", " ")
	return b.Path, os.WriteFile(b.Path, raw, 0o600)
}

type ApplyResult struct {
	Backup   string              `json:"backup"`
	Applied  []TargetPlan        `json:"applied"`
	Verified map[string]bool     `json:"verified"`
	Restored map[string][]string `json:"restored,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// Apply backs up every target list, adds exactly the planned entities and
// re-reads each list to verify; any failure restores all targets from the
// backup. The plan digest must match a fresh plan, so a stale plan never runs.
func Apply(ctx context.Context, api API, digestWant string, targets, only []string) (ApplyResult, error) {
	var res ApplyResult
	in, err := Load(ctx, api)
	if err != nil {
		return res, err
	}
	p, err := Plan(in, targets, only)
	if err != nil {
		return res, err
	}
	if p.Digest != digestWant {
		return res, errors.New("plan changed since review: run plan again")
	}
	// Back up only the planned targets: a later restore must never rewrite
	// exports this apply did not select (Alice when only HomeKit is chosen).
	b := Backup{At: time.Now().UTC(), Exports: map[string][]string{}}
	for _, ex := range in.Exports {
		for _, t := range p.Targets {
			if ex.Error == "" && t.EntryID == ex.EntryID {
				b.Exports[ex.EntryID] = ex.Entities
			}
		}
	}
	if res.Backup, err = saveBackup(b); err != nil {
		return res, err
	}
	res.Verified = map[string]bool{}
	fail := func(e error) (ApplyResult, error) {
		res.Error = e.Error()
		res.Restored = map[string][]string{}
		for _, t := range res.Applied {
			if _, rerr := SetExport(ctx, api, t.EntryID, Routes[t.Domain], b.Exports[t.EntryID]); rerr == nil {
				res.Restored[t.EntryID] = b.Exports[t.EntryID]
			}
		}
		return res, e
	}
	for _, t := range p.Targets {
		if len(t.Add) == 0 {
			continue
		}
		res.Applied = append(res.Applied, t)
		want := append(slices.Clone(b.Exports[t.EntryID]), t.Add...)
		if _, err := SetExport(ctx, api, t.EntryID, Routes[t.Domain], want); err != nil {
			return fail(err)
		}
		got, err := ReadExport(ctx, api, t.EntryID, Routes[t.Domain])
		if err != nil || !slices.Equal(uniqueSorted(got), uniqueSorted(want)) {
			return fail(fmt.Errorf("%s did not keep the new list", t.Title))
		}
		res.Verified[t.EntryID] = true
	}
	return res, nil
}

// Restore puts every export list of a backup file back.
func Restore(ctx context.Context, api API, path string) (map[string][]string, error) {
	if filepath.Dir(path) != BackupDir {
		return nil, errors.New("backup must be one of " + BackupDir)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b Backup
	if json.Unmarshal(raw, &b) != nil {
		return nil, errors.New("backup unreadable")
	}
	entries, err := ConfigEntries(ctx, api)
	if err != nil {
		return nil, err
	}
	done := map[string][]string{}
	for _, e := range entries {
		list, ok := b.Exports[e.EntryID]
		if !ok {
			continue
		}
		if _, err := SetExport(ctx, api, e.EntryID, Routes[e.Domain], list); err != nil {
			return done, err
		}
		done[e.EntryID] = list
	}
	return done, nil
}

type EntityChange struct {
	Entity string  `json:"entity_id"`
	Name   *string `json:"name,omitempty"`
	AreaID *string `json:"area_id,omitempty"`
}

// UpdateEntity renames an entity or moves it to an area without changing its
// entity_id; it returns the previous values for rollback and verifies the
// registry afterwards.
func UpdateEntity(ctx context.Context, api API, c EntityChange) (before EntityChange, err error) {
	res, err := api.WS(ctx, map[string]any{"type": "config/entity_registry/get", "entity_id": c.Entity})
	if err != nil {
		return before, err
	}
	var cur struct {
		Name   *string `json:"name"`
		AreaID *string `json:"area_id"`
	}
	if json.Unmarshal(res[0], &cur) != nil {
		return before, errors.New("entity registry entry unreadable")
	}
	before = EntityChange{Entity: c.Entity, Name: cur.Name, AreaID: cur.AreaID}
	cmd := map[string]any{"type": "config/entity_registry/update", "entity_id": c.Entity}
	// An empty value resets to the integration's default (null in HA).
	for k, v := range map[string]*string{"name": c.Name, "area_id": c.AreaID} {
		if v != nil {
			cmd[k] = any(*v)
			if *v == "" {
				cmd[k] = nil
			}
		}
	}
	if _, err = api.WS(ctx, cmd); err != nil {
		return before, err
	}
	res, err = api.WS(ctx, map[string]any{"type": "config/entity_registry/get", "entity_id": c.Entity})
	if err != nil || json.Unmarshal(res[0], &cur) != nil {
		return before, errors.New("could not verify the change")
	}
	if (c.Name != nil && str(cur.Name) != *c.Name) || (c.AreaID != nil && str(cur.AreaID) != *c.AreaID) {
		return before, errors.New("registry did not keep the change")
	}
	return before, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
