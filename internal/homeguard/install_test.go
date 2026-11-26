package homeguard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverHomeUnits(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte("docker.service loaded active running\ncontainerd.service loaded active running\nlane-api.service loaded inactive dead\nlaned.service loaded active running\nlane-exit-awg@vps.service loaded active running\nlane-deleted.service not-found inactive dead\ndnsmasq.service loaded active running\ntailscaled.service loaded active running\nevil/../../x.service loaded active running\nother.service loaded active running\n"), nil
	}
	got, err := HomeUnits(context.Background(), run)
	if err != nil || strings.Join(got, ",") != "containerd.service,dnsmasq.service,docker.service,lane-api.service,lane-exit-awg@vps.service,laned.service,tailscaled.service" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err = HomeUnits(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("offline") }); err == nil {
		t.Fatal("discovery failure ignored")
	}
}
func TestInstallAndRollback(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "backup.json")
	path := filepath.Join(root, "lane-api.service.d", "50-gateway-priority.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	old := []byte("[Service]\nCPUWeight=800\n")
	if err := os.WriteFile(path, old, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	units := "lane-api.service loaded active running\n"
	reloads := 0
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "show" {
			return []byte("CPUWeight=800\nIOWeight=700\nCPUQuotaPerSecUSec=2s\n"), nil
		}
		if args[0] == "daemon-reload" {
			reloads++
			return nil, nil
		}
		return []byte(units), nil
	}
	for range 2 {
		if err := Install(context.Background(), run, root, state, false); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "[Service]\nCPUWeight=1000\nIOWeight=1000\n" {
		t.Fatalf("%q", raw)
	}
	slice, _ := os.ReadFile(filepath.Join(root, "mcpshka-heavy.slice"))
	for _, s := range []string{"[Slice]", "CPUWeight=20", "IOWeight=10", "CPUQuota=300%"} {
		if !strings.Contains(string(slice), s) {
			t.Fatalf("slice: %q", slice)
		}
	}
	// A disappeared unit is restored without querying or mutating that unit.
	units = ""
	if err := Install(context.Background(), run, root, state, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != string(old) {
		t.Fatalf("removed unit not restored: %q", raw)
	}
	for range 2 {
		if err := Install(context.Background(), run, root, state, true); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ = os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(raw) != string(old) || info.Mode().Perm() != 0640 {
		t.Fatal("backup not restored")
	}
	if _, err := os.Stat(filepath.Join(root, "mcpshka-heavy.slice")); !os.IsNotExist(err) {
		t.Fatal("slice not removed")
	}
	if reloads == 0 {
		t.Fatal("missing reload")
	}
}
func TestRollbackPreservesForeignChange(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "backup.json")
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "show" {
			return []byte("CPUWeight=800\nIOWeight=700\nCPUQuotaPerSecUSec=2s\n"), nil
		}
		return nil, nil
	}
	if err := Install(context.Background(), run, root, state, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "mcpshka-heavy.slice")
	if err := os.WriteFile(path, []byte("owner change"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), run, root, state, true); err == nil {
		t.Fatal("overwrote owner change")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "owner change" {
		t.Fatal("changed owner bytes")
	}
}

// MCP-219: a deliberately masked DNS service must receive no policy writes,
// runtime properties or start, including when an older backup mentions it.
func TestMaskedAndMissingHomeUnitsAreSkipped(t *testing.T) {
	for _, load := range []string{"masked", "not-found"} {
		t.Run(load, func(t *testing.T) {
			root, state := t.TempDir(), filepath.Join(t.TempDir(), "backup.json")
			run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "/usr/bin/docker" {
					return nil, errors.New("not running")
				}
				switch args[0] {
				case "list-units":
					return []byte("dnsmasq.service " + load + " inactive dead\n"), nil
				case "show":
					if args[1] == "--property=LoadState" {
						return []byte(load), nil
					}
					return []byte("CPUWeight=100\nIOWeight=100\nCPUQuotaPerSecUSec=infinity\n"), nil
				}
				if strings.Contains(strings.Join(args, " "), "dnsmasq") {
					t.Fatalf("touched unavailable DNS: %v", args)
				}
				return nil, nil
			}
			if err := Install(context.Background(), run, root, state, false); err != nil {
				t.Fatal(err)
			}
			policy := filepath.Join(root, "dnsmasq.service.d", homeDropIn)
			if _, err := os.Stat(policy); !os.IsNotExist(err) {
				t.Fatal("installed DNS policy", err)
			}
			if err := replaceFile(policy, []byte(homeText), 0644); err != nil {
				t.Fatal(err)
			}
			// This entry models an old installation made before the unit was masked.
			if err := withBackup(state, func(b *backup, _ bool) error {
				b.Runtime["dnsmasq.service"] = []string{"CPUWeight=100", "IOWeight=100"}
				b.Files["dnsmasq.service.d/"+homeDropIn] = savedFile{After: homeText}
				raw, _ := json.Marshal(b)
				return replaceFile(state, raw, 0600)
			}); err != nil {
				t.Fatal(err)
			}
			if err := Weights(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if err := Install(context.Background(), run, root, state, true); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(policy); err != nil || string(raw) != homeText {
				t.Fatal("DNS policy changed", string(raw), err)
			}
			if _, err := os.Stat(state); err != nil {
				t.Fatal("lost skipped policy backup", err)
			}
		})
	}
}
