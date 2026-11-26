package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRunnerProtocol(t *testing.T) {
	bin := t.TempDir()
	for name, script := range map[string]string{
		"go":      "#!/bin/sh\nif [ \"$1\" = env ]; then echo go1.26.0; exit; fi\ncat fixture; exit 7\n",
		"sqlite3": "#!/bin/sh\nexit 0\n",
		"python3": "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	conf := filepath.Join(t.TempDir(), "runner.json")
	payload := archive(t, "fixture", "output from the unpacked job")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var results atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		if r.URL.Path != "/runner/register" && r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("missing runner key")
		}
		switch r.URL.Path {
		case "/runner/register":
			if r.Method != http.MethodPost || string(body["token"]) != "\"fixture-token\"" || r.Header.Get("Authorization") != "" {
				t.Error("registration protocol")
			}
			_, _ = w.Write([]byte(`{"id":"fixture-id","key":"fixture-key"}`))
		case "/runner/poll":
			if r.Method != http.MethodPost || !strings.Contains(string(body["caps"]), `"pyyaml":"1"`) {
				t.Errorf("bad capabilities: %s", body["caps"])
			}
			if results.Load() == 0 {
				_, _ = w.Write([]byte(`{"job":{"id":"rj-fixture","argv":["go","test","./..."],"timeout_seconds":2}}`))
			} else {
				cancel()
			}
		case "/runner/archive":
			if r.Method != http.MethodGet || r.URL.Query().Get("job_id") != "rj-fixture" {
				t.Error("archive protocol")
			}
			_, _ = w.Write(payload)
		case "/runner/result":
			if r.Method != http.MethodPost || string(body["job_id"]) != "\"rj-fixture\"" || string(body["exit_code"]) != "7" ||
				string(body["output"]) != "\"output from the unpacked job\"" || string(body["error"]) != "\"\"" {
				t.Errorf("bad result: %s", body)
			}
			results.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer s.Close()
	t.Setenv("MCPSHKA_RUNNER_TOKEN", "fixture-token")
	_ = run(ctx, []string{"--url", s.URL + "/", "--conf", conf})
	if results.Load() != 1 {
		t.Fatal("job result missing")
	}
	info, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions", info.Mode())
	}
	cfg, err := os.ReadFile(conf)
	if err != nil || !bytes.Contains(cfg, []byte(s.URL)) {
		t.Fatal("registration did not persist the origin", err)
	}
}

func TestHeartbeatCancelsJob(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nsleep 20 & wait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := archive(t, "fixture", "")
	completed := make(chan result, 1)
	var beats atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runner/archive":
			_, _ = w.Write(payload)
		case "/runner/heartbeat":
			var in map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil || in["job_id"] != "rj-cancel" || r.Header.Get("Authorization") != "Bearer key" {
				t.Error("heartbeat protocol")
			}
			beats.Add(1)
			_, _ = w.Write([]byte(`{"cancel":true}`))
		case "/runner/result":
			var res result
			if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
				t.Error(err)
			}
			completed <- res
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r := runner{base: s.URL, key: "key", tools: toolchain{bin: bin}, heartbeat: 250 * time.Millisecond}
	r.handle(ctx, &job{ID: "rj-cancel", Argv: []string{"go", "test"}, Timeout: 10})
	select {
	case res := <-completed:
		if res.Error != "cancelled by MCPshka" || res.Code != -1 || beats.Load() == 0 {
			t.Fatalf("cancellation result %+v", res)
		}
	default:
		t.Fatal("cancelled job did not report to hub")
	}
}

func TestDuplicateStartExitsBeforeRegistration(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "runner.json")
	lock, err := os.OpenFile(conf+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"--url", "https://fixture.invalid", "--conf", conf}); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackRejectsTraversal(t *testing.T) {
	if err := unpack(t.TempDir(), bytes.NewReader(archive(t, "../escape", "unsafe"))); err == nil {
		t.Fatal("unsafe archive accepted")
	}
}

func archive(t *testing.T, name, raw string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(tarHeader(name, raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func tarHeader(name, raw string) *tar.Header {
	return &tar.Header{Name: name, Mode: 0600, Size: int64(len(raw))}
}
