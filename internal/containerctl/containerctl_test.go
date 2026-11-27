package containerctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const staleID = "6fde62d09438b3a4575f795806426526377df254b06ed47753c02b71e513e7b5"

// fakeDocker answers ps/inspect/stats/logs from a fixed host and lets a test
// script the start result.
type fakeDocker struct {
	calls []string
	start func() ([]byte, error)
}

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "ps":
		return []byte(`{"Names":"homeassistant","Image":"ha:1","State":"running","Status":"Up 2 days"}
{"Names":"zigbee2mqtt","Image":"z2m:2","State":"exited","Status":"Exited (128) 15 minutes ago"}
{"Names":"bad name!","Image":"x","State":"running","Status":"Up"}
`), nil
	case "inspect":
		return []byte(`{"name":"/homeassistant","restarts":0,"health":"healthy","started":"2026-11-06T00:00:00Z","policy":"unless-stopped","compose":""}
{"name":"/zigbee2mqtt","restarts":3,"health":"","started":"2026-11-08T15:38:00Z","policy":"unless-stopped","compose":"home"}
`), nil
	case "stats":
		return []byte(`{"Name":"homeassistant","CPUPerc":"5.30%","MemUsage":"400MiB / 3.7GiB"}`), nil
	case "start":
		return f.start()
	case "logs":
		return []byte("2026-11-08T18:47:54Z \x1b[31merror\x1b[39m: Adapter disconnected\n"), nil
	}
	return nil, nil
}

func TestListIsLiveAndProblemsFirst(t *testing.T) {
	f := &fakeDocker{}
	list, err := List(context.Background(), f.run)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	if list[0].Name != "zigbee2mqtt" || list[0].Restarts != 3 || list[0].Compose != "home" {
		t.Fatal("stopped container not first or details lost", list[0])
	}
	if list[1].Health != "healthy" || list[1].CPU != "5.30%" {
		t.Fatal("health or usage missing", list[1])
	}
}

func TestActRefusesUnknownAndBadInput(t *testing.T) {
	f := &fakeDocker{}
	for _, c := range [][2]string{{"ghost", "start"}, {"zigbee2mqtt", "rm"}, {"-rf", "stop"}} {
		if _, err := Act(context.Background(), f.run, c[0], c[1]); err == nil {
			t.Fatal("accepted", c)
		}
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "rm") || strings.HasPrefix(call, "start") {
			t.Fatal("refused request reached docker:", call)
		}
	}
}

func staleSetup(t *testing.T, pid string) string {
	t.Helper()
	tmp := t.TempDir()
	TaskRoot, StaleRoot = filepath.Join(tmp, "moby"), filepath.Join(tmp, "stale")
	dir := filepath.Join(TaskRoot, staleID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.pid"), []byte(pid), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 08.11: docker start zigbee2mqtt → "mkdir …/moby/<id>: file exists" because
// the dead task directory stayed behind. The start heals it once.
func TestStartHealsDeadContainerdTask(t *testing.T) {
	dir := staleSetup(t, "424242")
	old := procAlive
	procAlive = func(int) bool { return false }
	t.Cleanup(func() { procAlive = old })
	attempts := 0
	f := &fakeDocker{start: func() ([]byte, error) {
		attempts++
		if attempts == 1 {
			return []byte("Error response from daemon: failed to create task for container: mkdir /run/containerd/io.containerd.runtime.v2.task/moby/" + staleID + ": file exists"), errors.New("exit status 1")
		}
		return []byte("zigbee2mqtt"), nil
	}}
	res, err := Act(context.Background(), f.run, "zigbee2mqtt", "start")
	if err != nil || attempts != 2 || res.Healed == "" {
		t.Fatal("start not healed", res, err, attempts)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dead task dir still in place")
	}
	if _, err := os.Stat(filepath.Join(res.Healed, "init.pid")); err != nil {
		t.Fatal("dead task dir was not preserved aside", err)
	}
}

func TestStartNeverMovesALiveTask(t *testing.T) {
	dir := staleSetup(t, "1")
	old := procAlive
	procAlive = func(int) bool { return true }
	t.Cleanup(func() { procAlive = old })
	f := &fakeDocker{start: func() ([]byte, error) {
		return []byte("mkdir /run/containerd/io.containerd.runtime.v2.task/moby/" + staleID + ": file exists"), errors.New("exit status 1")
	}}
	if _, err := Act(context.Background(), f.run, "zigbee2mqtt", "start"); err == nil {
		t.Fatal("start reported success")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("live task dir was moved", err)
	}
}

func TestLogsStripColourAndDispatchShapes(t *testing.T) {
	f := &fakeDocker{}
	raw, err := Dispatch(context.Background(), f.run, []string{"logs", "zigbee2mqtt", "50"})
	if err != nil || strings.Contains(string(raw), `\u001b`) || !strings.Contains(string(raw), "Adapter disconnected") {
		t.Fatal(string(raw), err)
	}
	if _, err := Dispatch(context.Background(), f.run, []string{"exec", "zigbee2mqtt", "sh"}); err == nil {
		t.Fatal("exec accepted")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "logs") && !strings.HasPrefix(call, "logs --tail 50 --timestamps zigbee2mqtt") {
			t.Fatal("unexpected logs call", call)
		}
	}
}
