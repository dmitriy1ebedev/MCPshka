// Package dockerapi reads Docker over the Engine API socket. The docker CLI
// is a 40 MB Go binary that also wakes containerd/runc helpers; the broker
// and homeguard ask the same few things every few seconds, which is what a
// host already short of memory can least afford (MCP-159, MCP-164). Callers
// fall back to the CLI whenever the socket does not answer.
package dockerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const maxBody = 1 << 20

// Socket is swapped by tests.
var Socket = "/run/docker.sock"

var client = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", Socket)
		},
		MaxIdleConns:    1,
		IdleConnTimeout: 2 * time.Minute,
	},
}

// ErrNotFound mirrors the CLI failing on a missing container.
var ErrNotFound = errors.New("docker: no such container")

// Get returns the body of one GET; a 404 is ErrNotFound.
func Get(ctx context.Context, path string) ([]byte, error) { return do(ctx, http.MethodGet, path, nil) }

func do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	switch {
	case err != nil:
		return nil, err
	case len(body) > maxBody:
		return nil, errors.New("docker: response too large")
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("docker: HTTP %d", resp.StatusCode)
	}
	return body, nil
}

var psArgs = []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}"}

// PS answers `docker ps -a --no-trunc --format {{json .}}` with the fields
// every reader uses — Image, Names, State and Docker's own human Status —
// in the CLI's order; ok=false (other args, dead socket) means: run the CLI.
func PS(ctx context.Context, args []string) ([]byte, bool) {
	if !slices.Equal(args, psArgs) {
		return nil, false
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := Get(c, "/containers/json?all=1")
	var list []struct {
		Names                []string
		Image, State, Status string
	}
	if err != nil || json.Unmarshal(body, &list) != nil {
		return nil, false
	}
	var out bytes.Buffer
	for _, ct := range list {
		names := make([]string, 0, len(ct.Names))
		for _, n := range ct.Names {
			names = append(names, strings.TrimPrefix(n, "/"))
		}
		row, err := json.Marshal(struct{ Image, Names, State, Status string }{ct.Image, strings.Join(names, ","), ct.State, ct.Status})
		if err != nil {
			return nil, false
		}
		out.Write(row)
		out.WriteByte('\n')
	}
	return out.Bytes(), true
}

// The two inspect templates the broker allows (dockerops Status/Inspect),
// as paths into the Engine API's container JSON.
var inspectPaths = map[string][][]string{
	"{{json .State}}": {{"State"}},
	"{{json .Id}}\n{{json .Config.Image}}\n{{json .HostConfig.RestartPolicy.Name}}\n{{json .Config.Labels}}\n{{json .NetworkSettings.Networks}}\n{{json .Mounts}}": {
		{"Id"}, {"Config", "Image"}, {"HostConfig", "RestartPolicy", "Name"}, {"Config", "Labels"}, {"NetworkSettings", "Networks"}, {"Mounts"}},
}

// Inspect answers `docker inspect --format <template> NAME` for those
// templates: one compact JSON row per field, as the CLI's json function
// prints it (the daemon encodes the same types in the same field order);
// a missing field is null. ok=false means: run the CLI.
func Inspect(ctx context.Context, args []string) ([]byte, bool) {
	if slices.Equal(args, []string{"mdns", "go2rtc"}) {
		raw, _ := json.Marshal(mdns(ctx))
		return raw, true
	}
	if len(args) != 4 || args[0] != "inspect" || args[1] != "--format" || strings.ContainsAny(args[3], "/?#%") {
		return nil, false
	}
	paths, known := inspectPaths[args[2]]
	if !known {
		return nil, false
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := Get(c, "/containers/"+args[3]+"/json")
	if err != nil {
		return nil, false
	}
	var out bytes.Buffer
	for _, path := range paths {
		raw := json.RawMessage(body)
		for _, key := range path {
			var obj map[string]json.RawMessage
			if json.Unmarshal(raw, &obj) != nil {
				return nil, false
			}
			if raw = obj[key]; raw == nil {
				raw = json.RawMessage("null")
				break
			}
		}
		if json.Compact(&out, raw) != nil {
			return nil, false
		}
		out.WriteByte('\n')
	}
	return out.Bytes(), true
}

// Exec runs argv in a running container like `docker exec` without a TTY:
// the output keeps Docker's stream headers, code is the exit status.
func Exec(ctx context.Context, container string, argv []string) (out []byte, code int, err error) {
	raw, err := do(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/exec",
		map[string]any{"Cmd": argv, "AttachStdout": true, "AttachStderr": true})
	var created struct{ Id string }
	if err != nil || json.Unmarshal(raw, &created) != nil || created.Id == "" {
		return nil, -1, errors.Join(err, errors.New("docker: exec not created"))
	}
	if out, err = do(ctx, http.MethodPost, "/exec/"+created.Id+"/start", map[string]bool{"Detach": false, "Tty": false}); err != nil {
		return nil, -1, err
	}
	raw, err = Get(ctx, "/exec/"+created.Id+"/json")
	var done struct {
		Running  bool
		ExitCode int
	}
	if err != nil || json.Unmarshal(raw, &done) != nil || done.Running {
		return out, -1, errors.Join(err, errors.New("docker: exec status unknown"))
	}
	return out, done.ExitCode, nil
}
