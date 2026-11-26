package homeguard

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"mcpshka/internal/dockerapi"
)

// The guard asks Docker the same three things every minute; they go to the
// Engine API socket (internal/dockerapi), anything else or a socket that
// does not answer still runs the CLI (MCP-159, MCP-164).

const (
	idTemplate     = "{{.Id}}"
	statusTemplate = "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}"
)

var (
	containerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	engineNow   = time.Now
)

// engineAnswer serves `docker inspect -f <id|status>`, `docker logs
// --since 2m --tail 400` and `docker exec NAME argv...`; ok=false means:
// run the CLI.
func engineAnswer(ctx context.Context, args []string) ([]byte, bool, error) {
	switch {
	case len(args) == 4 && args[0] == "inspect" && args[1] == "-f" && (args[2] == idTemplate || args[2] == statusTemplate) && containerRE.MatchString(args[3]):
		body, err := dockerapi.Get(ctx, "/containers/"+url.PathEscape(args[3])+"/json")
		if errors.Is(err, dockerapi.ErrNotFound) {
			return []byte("Error: No such object: " + args[3] + "\n"), true, err
		}
		if err != nil {
			return nil, false, nil
		}
		var c struct {
			ID    string `json:"Id"`
			State struct {
				Status string
				Health *struct{ Status string }
			}
		}
		if json.Unmarshal(body, &c) != nil || c.ID == "" || c.State.Status == "" {
			return nil, false, nil
		}
		if args[2] == idTemplate {
			return []byte(c.ID + "\n"), true, nil
		}
		health := ""
		if c.State.Health != nil {
			health = c.State.Health.Status
		}
		return []byte(c.State.Status + " " + health + "\n"), true, nil
	case len(args) == 6 && args[0] == "logs" && args[1] == "--since" && args[3] == "--tail" && containerRE.MatchString(args[5]):
		since, err := time.ParseDuration(args[2])
		tail, terr := strconv.Atoi(args[4])
		if err != nil || terr != nil || since <= 0 || tail <= 0 {
			return nil, false, nil
		}
		q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "since": {strconv.FormatInt(engineNow().Add(-since).Unix(), 10)}, "tail": {strconv.Itoa(tail)}}
		body, err := dockerapi.Get(ctx, "/containers/"+url.PathEscape(args[5])+"/logs?"+q.Encode())
		if errors.Is(err, dockerapi.ErrNotFound) {
			return []byte("Error: No such container: " + args[5] + "\n"), true, err
		}
		if err != nil {
			return nil, false, nil
		}
		return demux(body), true, nil
	case len(args) >= 3 && args[0] == "exec" && containerRE.MatchString(args[1]):
		out, code, err := dockerapi.Exec(ctx, args[1], args[2:])
		if code < 0 {
			return nil, false, nil
		}
		if err == nil && code != 0 {
			err = fmt.Errorf("exit status %d", code)
		}
		return demux(out), true, err
	}
	return nil, false, nil
}

// demux strips the 8-byte stream headers Docker adds to the logs of a
// container without a TTY; a TTY container's log is already plain.
func demux(b []byte) []byte {
	var out bytes.Buffer
	rest := b
	for len(rest) > 0 {
		if len(rest) < 8 || rest[0] > 2 || rest[1] != 0 || rest[2] != 0 || rest[3] != 0 {
			return b
		}
		n := int(binary.BigEndian.Uint32(rest[4:8]))
		if n > len(rest)-8 {
			return b
		}
		out.Write(rest[8 : 8+n])
		rest = rest[8+n:]
	}
	return out.Bytes()
}
