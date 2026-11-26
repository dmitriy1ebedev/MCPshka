// Package homeguard keeps the home ahead of heavy work (BUG-20261109-1:
// Zigbee2MQTT died under a go test run outside MCPshka limits). The root
// broker runs it: every minute it probes Zigbee2MQTT and MQTT and writes
// heavyqueue.HomeStatusFile, holding new heavy work while home is degraded.
// Coordinated upgrades install backed-up systemd policy; the broker refreshes
// runtime weights every five minutes. The off switch restores runtime weights;
// the home-priority --rollback command also restores persistent files.
package homeguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/heavyqueue"
)

const OffFile = "/etc/mcpshka/home-priority-off"

var (
	homeContainers = []string{"zigbee2mqtt", "mosquitto", "homeassistant", "matter-server"}
	probed         = []string{"zigbee2mqtt", "mosquitto"}
	adapterError   = regexp.MustCompile(`(?i)ASH_NCP_FATAL|ASH_ERROR|HOST_FATAL_ERROR|Adapter disconnected|adapter.*(timeout|timed out)`)
)

// Status is what the broker last observed; the heavy queue trusts it only
// while fresh.
type Status struct {
	State  string    `json:"state"` // ready | degraded
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
	// MQTTMillis is the last publish echo through mosquitto (0: not measured).
	MQTTMillis int `json:"mqtt_ms,omitempty"`
}

// Exec runs a host command; tests replace it.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

func hostExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "/usr/bin/docker" {
		if out, ok, err := engineAnswer(ctx, args); ok {
			return out, err
		}
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Run loops until ctx ends.
func Run(ctx context.Context, logf func(error)) {
	var lastWeights time.Time
	var mem MemSample
	reset := false
	for {
		if _, err := os.Stat(OffFile); err != nil {
			reset = false
			if time.Since(lastWeights) >= 5*time.Minute {
				if err := RuntimePolicy(ctx, hostExec, BackupFile, false); err != nil {
					logf(err)
				}
				lastWeights = time.Now()
			}
			now := time.Now()
			st := Probe(ctx, hostExec, now)
			if cur, err := ReadMem(now); err == nil {
				// The home first; memory trouble holds heavy work just the same (MCP-159).
				if why := Thrash(mem, cur); why != "" && st.State == "ready" {
					st = Status{State: "degraded", Reason: why, At: st.At}
				}
				if err := appendBaseline(pointOf(mem, cur)); err != nil {
					logf(err)
				}
				mem = cur
			}
			if err := Write(heavyqueue.HomeStatusFile, st); err != nil {
				logf(err)
			}
		} else {
			os.Remove(heavyqueue.HomeStatusFile)
			// The off switch is also the rollback: the weights go back to
			// their saved values, once, without waiting for a reboot.
			if !reset {
				if err := RuntimePolicy(ctx, hostExec, BackupFile, true); err != nil {
					logf(err)
				} else {
					reset, lastWeights = true, time.Time{}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// Probe calls the home degraded only while a running Zigbee2MQTT or MQTT is
// unhealthy or the coordinator logged adapter errors in the last two minutes.
// A container stopped on purpose holds nothing.
func Probe(ctx context.Context, run Exec, now time.Time) Status {
	st := Status{State: "ready", At: now.UTC()}
	bridge := false // a running Z2M without adapter errors: its bridge must say online
	for _, name := range probed {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := run(c, "/usr/bin/docker", "inspect", "-f", statusTemplate, name)
		cancel()
		f := strings.Fields(string(raw))
		if err != nil || len(f) == 0 || f[0] != "running" {
			continue
		}
		if len(f) > 1 && f[1] == "unhealthy" {
			return Status{State: "degraded", Reason: name + " unhealthy", At: st.At}
		}
		if name == "mosquitto" {
			if why := mqttEcho(ctx, run, &st); why != "" {
				return Status{State: "degraded", Reason: why, At: st.At, MQTTMillis: st.MQTTMillis}
			}
			continue
		}
		c, cancel = context.WithTimeout(ctx, 15*time.Second)
		logs, err := run(c, "/usr/bin/docker", "logs", "--since", "2m", "--tail", "400", name)
		cancel()
		if m := adapterError.Find(logs); err == nil && m != nil {
			return Status{State: "degraded", Reason: "zigbee2mqtt: " + string(m) + " in the last 2 min", At: st.At}
		}
		bridge = true
	}
	if bridge && st.MQTTMillis > 0 {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		state, err := run(c, "/usr/bin/docker", "exec", "mosquitto", "mosquitto_sub", "-h", "127.0.0.1", "-t", "zigbee2mqtt/bridge/state", "-C", "1", "-W", "2")
		cancel()
		if err == nil && bytes.Contains(state, []byte(`"offline"`)) {
			return Status{State: "degraded", Reason: "zigbee2mqtt bridge offline", At: st.At, MQTTMillis: st.MQTTMillis}
		}
	}
	return st
}

// mqttEcho publishes to a private topic and waits for the broker to deliver
// it back (mosquitto_rr subscribes to the reply topic first): the publish
// round trip the home's automations see. No echo in 3 s, or over 2 s, is
// degraded; the time is kept for owner_portal.
func mqttEcho(ctx context.Context, run Exec, st *Status) string {
	const topic = "mcpshka/homeguard/echo"
	c, cancel := context.WithTimeout(ctx, 6*time.Second)
	start := time.Now()
	out, err := run(c, "/usr/bin/docker", "exec", "mosquitto", "mosquitto_rr", "-h", "127.0.0.1", "-t", topic, "-e", topic, "-m", "ping", "-W", "3", "-N")
	took := time.Since(start)
	cancel()
	if err != nil || !bytes.Contains(out, []byte("ping")) {
		return "mqtt: no publish echo in 3 s"
	}
	st.MQTTMillis = max(int(took/time.Millisecond), 1)
	if took > 2*time.Second {
		return fmt.Sprintf("mqtt publish echo %d ms", st.MQTTMillis)
	}
	return ""
}

// Write replaces the status file atomically (group mcpshka reads it).
func Write(path string, st Status) error {
	raw, _ := json.Marshal(st)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".home-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Chmod(0o640)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
