package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestListIsBounded(t *testing.T) {
	run := func(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
		if !slices.Contains(args, "ps") {
			t.Fatalf("unexpected args %#v", args)
		}
		var rows []byte
		for _, name := range []string{"a", "b", "c"} {
			line, _ := json.Marshal(map[string]string{
				"Names": name, "Image": "img", "State": "running", "Status": "Up",
			})
			rows = append(rows, line...)
			rows = append(rows, '\n')
		}
		return rows, false, nil
	}
	out, err := newService(nil, nil, run).List(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Containers) != 2 || !out.Truncated {
		t.Fatalf("unexpected list: %+v", out)
	}
}

func TestListMixedWarningsAndJSON(t *testing.T) {
	run := func(context.Context, int, ...string) ([]byte, bool, error) {
		return []byte("WARNING: No swap limit support\n{\"Names\":\"homeassistant\",\"Image\":\"ha\",\"State\":\"running\",\"Status\":\"Up\"}\nWARN[0000] feature notice\n{\"Names\":\"mqtt\",\"Image\":\"mosquitto\",\"State\":\"running\",\"Status\":\"Up\"}\n"), false, nil
	}
	out, err := newService(nil, nil, run).List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Containers) != 2 || out.Containers[0].Name != "homeassistant" || len(out.Warnings) != 2 || out.Truncated {
		t.Fatalf("mixed docker output: %+v", out)
	}
}

func TestListUnknownTextFailsClosed(t *testing.T) {
	for _, raw := range []string{
		"Permission denied\n{\"Names\":\"demo\"}\n",
		"NOT_A_DOCKER_WARNING: injected\n{\"Names\":\"demo\"}\n",
	} {
		run := func(context.Context, int, ...string) ([]byte, bool, error) { return []byte(raw), false, nil }
		if _, err := newService(nil, nil, run).List(context.Background(), 10); err == nil {
			t.Fatalf("unexpected acceptance of %q", raw)
		}
	}
}

func TestListBoundsWarningEvidence(t *testing.T) {
	run := func(context.Context, int, ...string) ([]byte, bool, error) {
		return []byte(strings.Repeat("WARNING: "+strings.Repeat("a", 500)+"\n", 10) + "{\"Names\":\"demo\",\"State\":\"running\"}\n"), false, nil
	}
	out, err := newService(nil, nil, run).List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Warnings) != 8 || len(out.Warnings[0]) != 256 {
		t.Fatalf("warning limits: %+v", out)
	}
}

func TestListWarningsOnlyCannotMasqueradeAsEmptyInventory(t *testing.T) {
	for _, raw := range []string{
		"WARNING: Docker daemon notice\n",
		"WARNING Docker daemon notice\n",
		"WARN[0000] no usable data\n",
		"WARNING: Docker warning\n{\"Names\":\"../invalid\",\"State\":\"running\"}\n",
	} {
		run := func(context.Context, int, ...string) ([]byte, bool, error) { return []byte(raw), false, nil }
		out, err := newService(nil, nil, run).List(context.Background(), 10)
		if err == nil || len(out.Containers) != 0 {
			t.Fatalf("accepted warning-only/invalid inventory %q: %+v", raw, out)
		}
	}
	// A legitimate empty output with no warnings remains a valid empty inventory.
	run := func(context.Context, int, ...string) ([]byte, bool, error) { return nil, false, nil }
	if out, err := newService(nil, nil, run).List(context.Background(), 10); err != nil || len(out.Containers) != 0 {
		t.Fatalf("rejected empty verified stdout: %+v, %v", out, err)
	}
}

func TestStatusParsesHealth(t *testing.T) {
	run := func(context.Context, int, ...string) ([]byte, bool, error) {
		return []byte(`{"Status":"running","Running":true,"Restarting":false,"OOMKilled":false,"Dead":false,"Pid":42,"ExitCode":0,"Health":{"Status":"healthy"}}`), false, nil
	}
	out, err := newService(nil, nil, run).Status(context.Background(), "homeassistant")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Running || out.PID != 42 || out.HealthStatus != "healthy" {
		t.Fatalf("unexpected status: %+v", out)
	}
}

func TestStatusHandlesBoundedWarnings(t *testing.T) {
	const row = "{\"Status\":\"running\",\"Running\":true,\"Pid\":42,\"Health\":{\"Status\":\"healthy\"}}"
	for _, raw := range []string{
		"WARNING: Docker daemon warning\\n" + row,
		row + "\\nWARN[0000] daemon notice",
		"WARNING: first\\n" + row + "\\nWARNING: last",
	} {
		// Replace escaped line breaks in this test fixture with actual newlines.
		raw = strings.ReplaceAll(raw, "\\n", "\n")
		s := newService(nil, nil, func(context.Context, int, ...string) ([]byte, bool, error) {
			return []byte(raw), false, nil
		})
		got, err := s.Status(context.Background(), "homeassistant")
		if err != nil || !got.Running || got.PID != 42 || got.HealthStatus != "healthy" {
			t.Fatalf("unexpected state %+v, err %v", got, err)
		}
	}
}

func TestStatusRejectsAmbiguousDockerOutput(t *testing.T) {
	const row = "{\"Status\":\"running\",\"Running\":true}"
	for _, raw := range []string{
		"WARNING: only a warning",
		"WARNING: issue\\nnot JSON",
		row + "\\n" + row,
		"UNKNOWN_WARNING: injected\\n" + row,
		"WARNING: problem\\nnull",
		strings.Repeat("WARNING: notice\\n", 9) + row,
		"WARNING: " + strings.Repeat("x", 300) + "\\n" + row,
	} {
		raw = strings.ReplaceAll(raw, "\\n", "\n")
		s := newService(nil, nil, func(context.Context, int, ...string) ([]byte, bool, error) {
			return []byte(raw), false, nil
		})
		if got, err := s.Status(context.Background(), "homeassistant"); err == nil {
			t.Fatalf("unexpectedly accepted %q: %+v", raw, got)
		}
	}
}

func TestInspectMetadataWarningsAreBoundedAndRowsAreStrict(t *testing.T) {
	const rows = "\"abcdef1234567890\"\n\"ghcr.io/demo:latest\"\n\"unless-stopped\"\n" +
		"{\"com.docker.compose.project\":\"stack\"}\n{\"bridge\":{}}\n" +
		"[{\"Type\":\"bind\",\"Destination\":\"/data\"}]\n"
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "normal", raw: rows},
		{name: "warning prefix", raw: "WARNING: config read denied\n" + rows},
		{name: "warning middle", raw: "\"abcdef1234567890\"\nWARN[0000] config read denied\n" + strings.TrimPrefix(rows, "\"abcdef1234567890\"\n")},
		{name: "warning suffix", raw: rows + "WARNING: config read denied\n"},
		{name: "warnings only", raw: "WARNING: config read denied\n", wantErr: true},
		{name: "unknown text", raw: "permission denied\n" + rows, wantErr: true},
		{name: "too many warnings", raw: strings.Repeat("WARNING: notice\n", 9) + rows, wantErr: true},
		{name: "oversized warning", raw: "WARNING: " + strings.Repeat("x", 300) + "\n" + rows, wantErr: true},
		{name: "extra row", raw: rows + "\"unexpected\"\n", wantErr: true},
		{name: "missing row", raw: strings.TrimSuffix(rows, "[{\"Type\":\"bind\",\"Destination\":\"/data\"}]\n"), wantErr: true},
		{name: "invalid labels", raw: strings.Replace(rows, "{\"com.docker.compose.project\":\"stack\"}", "not-json", 1), wantErr: true},
		{name: "invalid networks", raw: strings.Replace(rows, "{\"bridge\":{}}", "not-json", 1), wantErr: true},
		{name: "invalid mounts", raw: strings.Replace(rows, "[{\"Type\":\"bind\",\"Destination\":\"/data\"}]", "not-json", 1), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(context.Context, int, ...string) ([]byte, bool, error) {
				return []byte(tc.raw), false, nil
			}
			got, err := newService(nil, nil, run).Inspect(context.Background(), "demo")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted invalid Docker inspect metadata: %+v", got)
				}
				return
			}
			if err != nil || got.ID != "abcdef123456" || got.ComposeProject != "stack" ||
				!slices.Contains(got.Networks, "bridge") || !slices.Contains(got.Mounts, "bind:/data") {
				t.Fatalf("unexpected inspect result %+v, err=%v", got, err)
			}
		})
	}
}

func TestInspectResourcesAndAction(t *testing.T) {
	var calls [][]string
	run := func(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "inspect" {
			if slices.Contains(args, "{{json .State}}") {
				return []byte(`{"Status":"running","Running":true}`), false, nil
			}
			return []byte(`"abcdef1234567890"
"ghcr.io/demo:latest"
"unless-stopped"
{"com.docker.compose.project":"stack","com.docker.compose.service":"app"}
{"bridge":{}}
[{"Type":"bind","Destination":"/data"}]
`), false, nil
		}
		if len(args) > 0 && args[0] == "stats" {
			return []byte(`{"Name":"demo","CPUPerc":"1.5%","MemUsage":"12MiB / 1GiB","MemPerc":"1.2%","NetIO":"1kB / 2kB","BlockIO":"3kB / 4kB","PIDs":"7"}`), false, nil
		}
		if len(args) > 0 && (args[0] == "start" || args[0] == "stop" || args[0] == "restart") {
			return []byte("demo\n"), false, nil
		}
		return nil, false, errors.New("unexpected")
	}
	s := newService(nil, map[string]struct{}{"demo": {}}, run)
	inspect, err := s.Inspect(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if inspect.ID != "abcdef123456" || inspect.ComposeProject != "stack" || inspect.ComposeService != "app" ||
		!slices.Contains(inspect.Networks, "bridge") || !slices.Contains(inspect.Mounts, "bind:/data") {
		t.Fatalf("inspect=%+v", inspect)
	}
	res, err := s.Resources(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if res.CPUPercent != "1.5%" || res.PIDs != "7" {
		t.Fatalf("resources=%+v", res)
	}
	for _, action := range []string{"start", "stop", "restart"} {
		if _, err := s.Action(context.Background(), "demo", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if _, err := s.Action(context.Background(), "demo", "rm"); err == nil {
		t.Fatal("destructive docker action accepted")
	}
	blocked := newService(nil, nil, run)
	if _, err := blocked.Action(context.Background(), "demo", "restart"); err == nil {
		t.Fatal("mutation without allowlist accepted")
	}
	if len(calls) < 8 {
		t.Fatalf("unexpected calls=%d", len(calls))
	}
}

func TestRestartRequiresAllowlist(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
		calls++
		if len(args) > 0 && args[0] == "restart" {
			return []byte("demo\n"), false, nil
		}
		if len(args) > 0 && args[0] == "inspect" {
			return []byte(`{"Status":"running","Running":true}`), false, nil
		}
		return nil, false, errors.New("unexpected")
	}
	allowed := map[string]struct{}{"demo": {}}
	restartOnly := newService(allowed, nil, run)
	out, err := restartOnly.Restart(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !out.Running {
		t.Fatalf("calls=%d status=%+v", calls, out)
	}
	if _, err := restartOnly.Action(context.Background(), "demo", "stop"); err == nil {
		t.Fatal("restart-only allowlist unexpectedly permits stop")
	}
	if _, err := newService(nil, nil, run).Restart(context.Background(), "demo"); err == nil {
		t.Fatal("restart without allowlist accepted")
	}
}

func TestEnvironmentMutationListIsSeparate(t *testing.T) {
	t.Setenv("MCPSHKA_DOCKER_RESTART_CONTAINERS", "legacy, ../bad")
	t.Setenv("MCPSHKA_DOCKER_MUTATION_CONTAINERS", "managed, ../bad")
	s := NewFromEnvironment()
	if _, ok := s.restartAllowed["legacy"]; !ok {
		t.Fatal("restart allowlist missing environment container")
	}
	if _, ok := s.mutationAllowed["managed"]; !ok {
		t.Fatal("mutation allowlist missing environment container")
	}
	if _, ok := s.mutationAllowed["legacy"]; ok {
		t.Fatal("legacy restart allowlist unexpectedly grants start/stop")
	}
}

func TestMountSourceEvidenceIsNotPublicJSON(t *testing.T) {
	run := func(context.Context, int, ...string) ([]byte, bool, error) {
		return []byte("\"1234567890123456\"\n\"image\"\n\"unless-stopped\"\n{}\n{}\n[{\"Type\":\"bind\",\"Source\":\"/root/repos/mcpshka\",\"Destination\":\"/data\"}]\n"), false, nil
	}
	inspect, err := newService(nil, nil, run).Inspect(context.Background(), "demo")
	if err != nil || len(inspect.MountSources) != 1 || inspect.MountSources[0] != "/root/repos/mcpshka" {
		t.Fatalf("missing internal mount source %+v %v", inspect, err)
	}
	raw, err := json.Marshal(inspect)
	if err != nil || strings.Contains(string(raw), "/root/repos/mcpshka") {
		t.Fatalf("raw host mounts escaped: %s %v", raw, err)
	}
}

func TestContainerNameValidation(t *testing.T) {
	for _, name := range []string{"homeassistant", "rootledger-app-1", "a.b_c"} {
		if !validContainerName(name) {
			t.Fatalf("valid name rejected %q", name)
		}
	}
	for _, name := range []string{"", "../x", "-bad", "name with space", "/x"} {
		if validContainerName(name) {
			t.Fatalf("invalid name accepted %q", name)
		}
	}
}
