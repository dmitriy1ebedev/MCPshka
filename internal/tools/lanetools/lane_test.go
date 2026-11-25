package lanetools

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/laneprovider"
)

type deadlineConn struct {
	net.Conn
	deadline time.Time
}

func (c *deadlineConn) SetDeadline(at time.Time) error { c.deadline = at; return nil }

func TestMCP240NativeRouterReadRoutingHasNoWriteOperation(t *testing.T) {
	cases := map[string]string{
		"router status":  "lane.router.status",
		"router wifi":    "lane.router.wifi",
		"router clients": "lane.router.clients",
		"router history": "lane.router.history",
		"device list":    "lane.device.list",
		"diag export":    "lane.diag.export",
	}
	for action, want := range cases {
		got, ok := nativeReadOperation(action)
		if !ok || got != want || !laneprovider.ReadOperations[got] {
			t.Fatalf("%q: got=%q allowed=%v", action, got, ok)
		}
	}
	for _, action := range []string{"router plan", "router apply", "router snapshot", "router restore", "router wifiwatch",
		"dns apply", "engines update", "service mode", "router device-ip"} {
		if got, ok := nativeReadOperation(action); ok {
			t.Fatalf("write/mutable read action %q escaped reader gate as %q", action, got)
		}
	}
}

func TestRequestDeadlineCoversLaneRuntime(t *testing.T) {
	c := &deadlineConn{}
	ctx, cancel := RequestContext(context.Background(), c, "lane", nil, time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 16*time.Minute || !c.deadline.After(deadline) {
		t.Fatal("Lane broker socket expires before its runtime")
	}
}

func TestFixedRecipesAndConfirmation(t *testing.T) {
	if Timeout("lane", nil, time.Second) != 17*time.Minute || Timeout("admin-session", []string{"lane"}, time.Second) != 17*time.Minute || Timeout("systemctl", nil, time.Second) != time.Second {
		t.Fatal("Lane timeout broadened other broker recipes")
	}
	for _, action := range []string{"status", "engines status", "router status", "router wifi", "router clients", "router history", "device list"} {
		args, stdin, mutation, err := recipe(Input{Action: action})
		if err != nil || mutation || len(args) == 0 || len(stdin) != 0 {
			t.Fatal(action, args, err)
		}
	}
	for since, ok := range map[string]bool{"": true, "24h": true, "0": true, "30m": true, "7d": false, "1h;rm": false} {
		args, _, mutation, err := recipe(Input{Action: "diag export", Since: since})
		if (err == nil) != ok || ok && (mutation || args[0] != "diag" || args[3] == "") {
			t.Fatal("diag export", since, args, err)
		}
	}
	if _, _, _, err := recipe(Input{Action: "status", Since: "24h"}); err == nil {
		t.Fatal("since accepted outside diag export")
	}
	confirm := strings.Repeat("a", 64)
	args, stdin, mutation, err := recipe(Input{Action: "router apply", Form: "wireless", Changes: map[string]string{"ssid": "home"}, Confirm: confirm})
	if err != nil || !mutation || !reflect.DeepEqual(args, []string{"router", "apply"}) {
		t.Fatal(args, err)
	}
	var payload map[string]any
	if json.Unmarshal(stdin, &payload) != nil || payload["confirm"] != confirm {
		t.Fatal(string(stdin))
	}
	for _, in := range []Input{
		{Action: "router apply", Form: "wireless", Changes: map[string]string{"ssid": "x"}},
		{Action: "router apply", Form: "wireless", Changes: map[string]string{"ssid": "x"}, Confirm: strings.Repeat("x", 64)},
		{Action: "status", Name: "--help"}, {Action: "router snapshot"}, {Action: "engines update", Name: "--all"},
		{Action: "dns commit", Tx: "../tx"}, {Action: "service mode", Name: "youtube", Mode: "direct"},
	} {
		if _, _, _, err := recipe(in); err == nil {
			t.Fatal("unsafe recipe accepted", in.Action)
		}
	}
	for _, in := range []Input{{Action: "engines update", Name: "tailscale"}, {Action: "dns apply"}, {Action: "dns commit", Tx: "tx-123"}, {Action: "service mode", Name: "youtube", Mode: "vpn"}} {
		_, _, mutation, err := recipe(in)
		if err != nil || !mutation {
			t.Fatal(in, err)
		}
		raw, _ := json.Marshal(in)
		if _, err := Read(context.Background(), []string{string(raw)}); err == nil || !strings.Contains(err.Error(), "FACE_ID_REQUIRED") {
			t.Fatal("mutation reached read route", err)
		}
	}
}

func TestFakeLaneBinaryReceivesStdinAndPreservesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lane")
	script := "#!/bin/sh\n[ \"$1 $2\" = 'router apply' ] || exit 7\ncat\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	in := []byte(`{"form":"wifi","changes":{"ssid":"masked"},"confirm":"fixture"}`)
	out, err := execute(context.Background(), path, []string{"router", "apply"}, in)
	if err != nil || string(out) != string(in) {
		t.Fatal(string(out), err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '{\"ok\":false,\"code\":\"BE900_SESSION_BUSY\"}'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, err = execute(context.Background(), path, nil, nil)
	if err != nil || !strings.Contains(string(out), "BE900_SESSION_BUSY") {
		t.Fatal(string(out), err)
	}
	if _, err := execute(context.Background(), "/nonexistent/lane", nil, nil); err == nil {
		t.Fatal("missing binary accepted")
	}
}
