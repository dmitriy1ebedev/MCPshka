package releaseapply

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// MCP-190: the apply restarts the broker mid-wait; the worker's report must
// survive in a file, and an older attempt's report must not answer.
func TestResultSurvivesAndIsFresh(t *testing.T) {
	ResultDir = t.TempDir()
	start := time.Now()
	if _, ok := Result("0.13.1", start); ok {
		t.Fatal("no report yet")
	}
	if err := SaveResult("0.13.1", []byte(`{"state":"APPLYING"}`)); err != nil {
		t.Fatal(err)
	}
	raw, ok := Result("0.13.1", start)
	if !ok || string(raw) != `{"state":"APPLYING"}` {
		t.Fatal(string(raw), ok)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(ResultDir, "0.13.1.json"), old, old)
	if _, ok := Result("0.13.1", start); ok {
		t.Fatal("stale report accepted")
	}
	os.WriteFile(filepath.Join(ResultDir, "0.13.2.json"), []byte("not json"), 0o600)
	if _, ok := Result("0.13.2", start.Add(-time.Minute)); ok {
		t.Fatal("invalid report accepted")
	}
}

func TestCurrentVersion(t *testing.T) {
	valid := regexp.MustCompile(`^0\.[0-9]+\.[0-9]+$`).MatchString
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"0.13.0"}`))
	}))
	defer srv.Close()
	HealthURL = srv.URL
	if v, err := CurrentVersion(valid); err != nil || v != "0.13.0" {
		t.Fatal(v, err)
	}
	HealthURL = srv.URL + "/x\x00"
	if _, err := CurrentVersion(valid); err == nil {
		t.Fatal("bad URL accepted")
	}
}
