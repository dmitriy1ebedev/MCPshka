package homeguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPreviousProperties(t *testing.T) {
	got, err := previousProperties("CPUWeight=[not set]\nIOWeight=700\nCPUQuotaPerSecUSec=2500ms", true)
	if err != nil || !reflect.DeepEqual(got, []string{"CPUWeight=", "IOWeight=700", "CPUQuota=250%"}) {
		t.Fatalf("%q %v", got, err)
	}
	for _, raw := range []string{"CPUWeight=0\nIOWeight=100", "CPUWeight=100\nIOWeight=oops", "CPUWeight=100\nIOWeight=100\nCPUWeight=100", "CPUWeight=100\nIOWeight=100\nCPUQuotaPerSecUSec=bad"} {
		if _, err := previousProperties(raw, true); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestRollbackRetriesReloadAndRestoresRuntime(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "backup.json")
	var calls []string
	fail := false
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "show":
			return []byte("CPUWeight=800\nIOWeight=700\nCPUQuotaPerSecUSec=2s"), nil
		case "list-units":
			return []byte("lane-api.service loaded active running"), nil
		case "daemon-reload":
			if fail {
				return nil, errors.New("reload failed")
			}
		}
		return nil, nil
	}
	if err := Install(context.Background(), run, root, state, false); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := Install(context.Background(), run, root, state, true); err == nil {
		t.Fatal("ignored reload failure")
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatal("lost backup", err)
	}
	fail = false
	if err := Install(context.Background(), run, root, state, true); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{"user.slice CPUWeight=800 IOWeight=700 CPUQuota=200%", "lane-api.service CPUWeight=800 IOWeight=700"} {
		if !strings.Contains(all, want) {
			t.Fatal("lost runtime original", want, all)
		}
	}
	before := len(calls)
	if err := Install(context.Background(), run, root, state, true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != before {
		t.Fatal("second rollback changed runtime")
	}
}
func TestInstallRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "backup.json")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "user.slice.d")); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "show" {
			return []byte("CPUWeight=100\nIOWeight=100\nCPUQuotaPerSecUSec=infinity"), nil
		}
		return nil, nil
	}
	if err := Install(context.Background(), run, root, state, false); err == nil {
		t.Fatal("followed symlink")
	}
	files, _ := os.ReadDir(outside)
	if len(files) != 0 {
		t.Fatal("wrote outside root")
	}
}

func TestRuntimePolicyRemembersNewContainersWithoutWritingUnits(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "backup.json")
	container := ""
	var changes []string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "show":
			return []byte("CPUWeight=800\nIOWeight=700\nCPUQuotaPerSecUSec=2s"), nil
		case "list-units":
			return []byte("lane-api.service loaded active running"), nil
		case "inspect":
			if args[len(args)-1] == "zigbee2mqtt" {
				return []byte(container), nil
			}
		case "set-property":
			changes = append(changes, strings.Join(args, " "))
		}
		return nil, nil
	}
	if err := Install(context.Background(), run, root, state, false); err != nil {
		t.Fatal(err)
	}
	slice := filepath.Join(root, "mcpshka-heavy.slice")
	original, _ := os.Stat(slice)
	container = strings.Repeat("a", 64)
	if err := RuntimePolicy(context.Background(), run, state, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(slice)
	if !after.ModTime().Equal(original.ModTime()) {
		t.Fatal("runtime rewrote persistent units")
	}
	if err := RuntimePolicy(context.Background(), run, state, true); err != nil {
		t.Fatal(err)
	}
	want := "docker-" + container + ".scope CPUWeight=800 IOWeight=700"
	if !strings.Contains(strings.Join(changes, "\n"), want) {
		t.Fatal("new container original lost", changes)
	}
}
