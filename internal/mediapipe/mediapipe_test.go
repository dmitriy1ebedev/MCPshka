package mediapipe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fake go2rtc keeps streams as name → sources.
func fake(t *testing.T) map[string][]string {
	t.Helper()
	st := map[string][]string{"cam": {"rtsp://user:secret@cam/stream2"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/streams":
			out := map[string]any{}
			for n, srcs := range st {
				var ps []map[string]string
				for _, s := range srcs {
					ps = append(ps, map[string]string{"url": s})
				}
				out[n] = map[string]any{"producers": ps, "consumers": []any{}}
			}
			json.NewEncoder(w).Encode(out)
		case "GET /api/config":
			w.Write([]byte("streams: {}\n"))
		case "PUT /api/streams":
			st[r.URL.Query().Get("name")] = r.URL.Query()["src"]
		case "DELETE /api/streams":
			delete(st, r.URL.Query().Get("src"))
		case "GET /api/stream.mjpeg":
			for i := 0; i < 3; i++ {
				w.Write([]byte{0xFF, 0xD8, 1, 2, 0xFF, 0xD9})
				w.(http.Flusher).Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	Base, StateDir = srv.URL, t.TempDir()
	return st
}

func TestApplyRollbackAndProtection(t *testing.T) {
	st := fake(t)
	ctx := t.Context()
	p := Profile{From: "cam", Video: "mjpeg", Width: 400, Height: 225}
	if err := Apply(ctx, "pip", p); err != nil {
		t.Fatal(err)
	}
	if got := st["pip"]; len(got) != 1 || got[0] != "ffmpeg:cam#video=mjpeg#width=400#height=225" {
		t.Fatalf("%v", got)
	}
	if err := Apply(ctx, "cam", p); err == nil || Delete(ctx, "cam") == nil {
		t.Fatal("source stream changed")
	}
	if err := Rollback(ctx, "pip"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st["pip"]; ok {
		t.Fatal("rollback did not remove the new stream")
	}
	if len(st["cam"]) != 1 {
		t.Fatal("source stream touched")
	}
}

func TestListMasksAndMeasure(t *testing.T) {
	fake(t)
	list, err := List(t.Context())
	if err != nil || len(list) != 1 || strings.Contains(list[0].Sources[0], "secret") {
		t.Fatalf("%v %v", list, err)
	}
	if m := Measure(t.Context(), "cam", 50*time.Millisecond); m.Frames != 3 || m.Format != "mjpeg" {
		t.Fatalf("%+v", m)
	}
	if _, err := (Profile{From: "cam", Video: "copy", Width: 10}).Source(); err == nil {
		t.Fatal("copy with resize accepted")
	}
}
