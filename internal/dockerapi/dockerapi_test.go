package dockerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The Engine API listing becomes the CLI's JSON rows: names without the
// leading slash, Docker's own Status.
func TestStatusRowsMatchCLIShape(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here")
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json" && r.URL.Query().Get("all") == "1":
			w.Write([]byte(`[{"Id":"x","Names":["/homeassistant"],"Image":"ghcr.io/home-assistant/home-assistant:stable","State":"running","Status":"Up 3 days (healthy)"},{"Names":["/torrserver"],"Image":"torrserver","State":"exited","Status":"Exited (0) 2 hours ago"}]`))
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	old := Socket
	Socket = sock
	t.Cleanup(func() { Socket = old })

	raw, ok := PS(context.Background(), psArgs)
	if !ok {
		t.Fatal("engine listing refused")
	}
	want := `{"Image":"ghcr.io/home-assistant/home-assistant:stable","Names":"homeassistant","State":"running","Status":"Up 3 days (healthy)"}` + "\n" +
		`{"Image":"torrserver","Names":"torrserver","State":"exited","Status":"Exited (0) 2 hours ago"}` + "\n"
	if string(raw) != want {
		t.Fatalf("rows:\n%s", raw)
	}
	if _, err := Get(context.Background(), "/containers/absent/json"); err != ErrNotFound {
		t.Fatalf("missing container: %v", err)
	}
	if _, ok := PS(context.Background(), []string{"ps", "-a"}); ok {
		t.Fatal("other ps form answered")
	}
	client.CloseIdleConnections()
	Socket = filepath.Join(t.TempDir(), "none.sock")
	if _, ok := PS(context.Background(), psArgs); ok {
		t.Fatal("dead socket answered")
	}
}

// On a host with Docker the API answer carries the same JSON as the CLI for
// both broker templates (compared as values: the CLI may order map keys).
func TestInspectMatchesCLI(t *testing.T) {
	if _, err := os.Stat(Socket); err != nil {
		t.Skip("no docker socket")
	}
	ps, ok := PS(context.Background(), psArgs)
	if !ok {
		t.Skip("docker socket not readable here")
	}
	var row struct{ Names string }
	if json.Unmarshal(bytes.SplitN(ps, []byte("\n"), 2)[0], &row) != nil || row.Names == "" {
		t.Skip("no containers")
	}
	name := strings.Split(row.Names, ",")[0]
	for tpl := range inspectPaths {
		api, ok := Inspect(context.Background(), []string{"inspect", "--format", tpl, name})
		if !ok {
			t.Fatalf("%s: API refused", tpl)
		}
		cli, err := exec.Command("/usr/bin/docker", "inspect", "--format", tpl, name).Output()
		if err != nil {
			t.Skipf("docker CLI: %v", err)
		}
		a, c := strings.Split(strings.TrimSpace(string(api)), "\n"), strings.Split(strings.TrimSpace(string(cli)), "\n")
		if len(a) != len(c) {
			t.Fatalf("%s: %d rows vs CLI %d", tpl, len(a), len(c))
		}
		for i := range a {
			var x, y any
			if json.Unmarshal([]byte(a[i]), &x) != nil || json.Unmarshal([]byte(c[i]), &y) != nil || !reflect.DeepEqual(x, y) {
				t.Fatalf("%s row %d:\napi %s\ncli %s", tpl, i, a[i], c[i])
			}
		}
		if string(api) != string(cli) {
			t.Logf("%s: same values, different byte order", tpl)
		}
	}
	if _, ok := Inspect(context.Background(), []string{"inspect", "--format", "{{json .Config.Env}}", name}); ok {
		t.Fatal("unknown template answered from the API")
	}
}

// Exec on the live daemon: the output and exit code `docker exec` gives.
func TestExecLive(t *testing.T) {
	ps, ok := PS(context.Background(), psArgs)
	if !ok {
		t.Skip("docker socket not readable here")
	}
	var name string
	for _, line := range bytes.Split(bytes.TrimSpace(ps), []byte("\n")) {
		var row struct{ Names, State string }
		if json.Unmarshal(line, &row) == nil && row.State == "running" {
			name = strings.Split(row.Names, ",")[0]
			break
		}
	}
	if name == "" {
		t.Skip("no running container")
	}
	out, code, err := Exec(context.Background(), name, []string{"sh", "-c", "echo hi; exit 3"})
	if err != nil {
		t.Skipf("exec unavailable in %s: %v", name, err)
	}
	if code != 3 || !bytes.Contains(out, []byte("hi")) {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
