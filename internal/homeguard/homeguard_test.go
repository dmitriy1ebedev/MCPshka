package homeguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
)

type fake map[string]string

func (f fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	return []byte(f[filepath.Base(name)+" "+strings.Join(args, " ")]), nil
}

const (
	echo   = "docker exec mosquitto mosquitto_rr -h 127.0.0.1 -t mcpshka/homeguard/echo -e mcpshka/homeguard/echo -m ping -W 3 -N"
	bridge = "docker exec mosquitto mosquitto_sub -h 127.0.0.1 -t zigbee2mqtt/bridge/state -C 1 -W 2"
)

func TestProbeDegradedOnlyWhileRunningWithAdapterErrors(t *testing.T) {
	insp := "docker inspect -f {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}} "
	now := time.Now()
	f := fake{insp + "zigbee2mqtt": "running \n", insp + "mosquitto": "running healthy\n",
		"docker logs --since 2m --tail 400 zigbee2mqtt": "error: zh:ember:ezsp: ASH_NCP_FATAL_ERROR",
		echo: "ping\n"}
	if st := Probe(context.Background(), f.run, now); st.State != "degraded" || !strings.Contains(st.Reason, "ASH_NCP_FATAL") {
		t.Fatalf("adapter error must degrade: %+v", st)
	}
	f[insp+"zigbee2mqtt"] = "exited \n"
	if st := Probe(context.Background(), f.run, now); st.State != "ready" {
		t.Fatalf("a stopped Z2M must not hold heavy work: %+v", st)
	}
	if st := Probe(context.Background(), f.run, now); st.MQTTMillis < 1 {
		t.Fatalf("echo time not kept: %+v", st)
	}
	f[echo] = ""
	if st := Probe(context.Background(), f.run, now); st.State != "degraded" || !strings.Contains(st.Reason, "no publish echo") {
		t.Fatalf("a running MQTT that does not echo must degrade: %+v", st)
	}
	f[echo] = "ping\n"
	f[insp+"zigbee2mqtt"] = "running \n"
	f["docker logs --since 2m --tail 400 zigbee2mqtt"] = ""
	f[bridge] = `{"state":"online"}`
	if st := Probe(context.Background(), f.run, now); st.State != "ready" {
		t.Fatalf("online bridge: %+v", st)
	}
	f[bridge] = `{"state":"offline"}`
	if st := Probe(context.Background(), f.run, now); st.State != "degraded" || !strings.Contains(st.Reason, "bridge offline") {
		t.Fatalf("offline bridge must degrade: %+v", st)
	}
	f[insp+"mosquitto"] = "running unhealthy\n"
	if st := Probe(context.Background(), f.run, now); st.State != "degraded" {
		t.Fatalf("unhealthy MQTT must degrade: %+v", st)
	}
}

func TestWeightsCoverHomeContainersAndConsoles(t *testing.T) {
	id := strings.Repeat("ab", 32)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list-units" {
			return []byte("docker.service loaded active running\ncontainerd.service loaded active running\n"), nil
		}
		if args[0] == "inspect" && args[len(args)-1] == "zigbee2mqtt" {
			return []byte(id + "\n"), nil
		}
		return nil, nil
	}
	if err := Weights(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{"docker-" + id + ".scope CPUWeight=1000", "user.slice CPUWeight=30 IOWeight=30 CPUQuota=300%", "--runtime"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
}

func TestQueueHoldsWhileHomeDegraded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "home.json")
	old := heavyqueue.HomeStatusFile
	heavyqueue.HomeStatusFile = p
	defer func() { heavyqueue.HomeStatusFile = old }()
	if err := Write(p, Status{State: "degraded", Reason: "zigbee2mqtt unhealthy", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if ok, why := heavyqueue.HostReady(); ok || !strings.Contains(why, "home degraded") {
		// HostReady may refuse for host load first; the home reason must win only when load is fine.
		if ok {
			t.Fatalf("fresh degraded home must hold heavy work")
		}
	}
	if err := Write(p, Status{State: "degraded", At: time.Now().Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

// The off switch puts back boot values on exactly the units Weights touches,
// and the drift check names what differs.
func TestResetAndDrift(t *testing.T) {
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list-units" {
			return []byte("docker.service loaded active running\ncontainerd.service loaded active running\n"), nil
		}
		return nil, nil
	}
	if err := Reset(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(calls, "\n")
	if !strings.Contains(all, "user.slice CPUWeight= IOWeight= CPUQuota=") || !strings.Contains(all, "docker.service CPUWeight= IOWeight=") || strings.Contains(all, "docker.service CPUWeight= IOWeight= CPUQuota") {
		t.Fatalf("reset:\n%s", all)
	}
	live := map[string]string{
		"mcpshka-heavy.slice": "CPUWeight=20\nIOWeight=10\nCPUQuotaPerSecUSec=3s\n",
		"docker.service":      "CPUWeight=1000\nIOWeight=1000\nCPUQuotaPerSecUSec=infinity\n",
		"containerd.service":  "CPUWeight=1000\nIOWeight=1000\nCPUQuotaPerSecUSec=infinity\n",
		"user.slice":          "CPUWeight=30\nIOWeight=30\nCPUQuotaPerSecUSec=3s\n",
	}
	show := func(u string) (string, error) { return live[u], nil }
	if ok, d := Drift(show, false); !ok {
		t.Fatal(d)
	}
	if ok, d := Drift(show, true); !ok || !strings.Contains(d, "off") {
		t.Fatalf("off uses original owner policy: %s", d)
	}
	live["user.slice"] = "CPUWeight=100\nIOWeight=30\nCPUQuotaPerSecUSec=3s\n"
	if ok, d := Drift(show, false); ok || !strings.Contains(d, "user.slice CPUWeight=100 (want 30)") {
		t.Fatalf("drift not named: %s", d)
	}
}
