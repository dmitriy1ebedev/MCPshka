package homeguard

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/dockerapi"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

// The guard's docker queries come from the Engine API socket with the CLI's
// spelling, so Probe reads them unchanged; a missing container fails like
// the CLI does.
func TestEngineAnswersGuardQueries(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here")
	}
	var since string
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/zigbee2mqtt/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Id":"d1d1bbaf39e2a73aaa5e143fbbba5bece9bc243c5e1a1a9e595d5e7d00f62aff","State":{"Status":"running","Health":{"Status":"healthy"}}}`))
	})
	mux.HandleFunc("/containers/mosquitto/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Id":"aa","State":{"Status":"running"}}`))
	})
	mux.HandleFunc("/containers/zigbee2mqtt/logs", func(w http.ResponseWriter, r *http.Request) {
		since = r.URL.Query().Get("since")
		if r.URL.Query().Get("tail") != "400" || r.URL.Query().Get("stderr") != "1" {
			http.Error(w, "bad query", 400)
			return
		}
		w.Write(append(frame(1, "z2m: started\n"), frame(2, "error: ASH_NCP_FATAL_ERROR\n")...))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	old, oldNow := dockerapi.Socket, engineNow
	dockerapi.Socket = sock
	engineNow = func() time.Time { return time.Unix(1_800_000_000, 0) }
	t.Cleanup(func() { dockerapi.Socket, engineNow = old, oldNow })
	ctx := context.Background()

	out, err := hostExec(ctx, "/usr/bin/docker", "inspect", "-f", idTemplate, "zigbee2mqtt")
	if err != nil || string(out) != "d1d1bbaf39e2a73aaa5e143fbbba5bece9bc243c5e1a1a9e595d5e7d00f62aff\n" {
		t.Fatalf("id %q %v", out, err)
	}
	if out, _ = hostExec(ctx, "/usr/bin/docker", "inspect", "-f", statusTemplate, "mosquitto"); string(out) != "running \n" {
		t.Fatalf("status without health %q", out)
	}
	if _, err = hostExec(ctx, "/usr/bin/docker", "inspect", "-f", statusTemplate, "absent"); err == nil {
		t.Fatal("missing container answered")
	}
	st := Probe(ctx, hostExec, time.Now())
	if st.State != "degraded" || !strings.Contains(st.Reason, "ASH_NCP_FATAL") {
		t.Fatalf("probe %+v", st)
	}
	if since != "1799999880" {
		t.Fatalf("since %q", since)
	}
	if _, ok, _ := engineAnswer(ctx, []string{"inspect", "-f", "{{.Config.Env}}", "zigbee2mqtt"}); ok {
		t.Fatal("unknown template served from the API")
	}
	if got := string(demux([]byte("plain tty log\n"))); got != "plain tty log\n" {
		t.Fatal(got)
	}
}
