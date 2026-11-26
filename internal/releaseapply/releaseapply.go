// Package releaseapply holds the root release worker's small host-side
// helpers that do not need the broker: reading the running version and
// keeping the worker's own receipt.
package releaseapply

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ResultDir keeps the last report of each release-apply worker. The apply it
// starts restarts the broker that waits for the report (MCP-190: the wait was
// cut and the call failed with an empty reason after a good install), so the
// broker reads the report from here instead of the dead pipe.
var ResultDir = "/var/lib/mcpshka-release-apply"

// HealthURL is the running controller's health endpoint.
var HealthURL = "http://127.0.0.1:8765/healthz"

// CurrentVersion asks the running controller; root never executes the
// service's binaries.
func CurrentVersion(valid func(string) bool) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(HealthURL)
	if err != nil {
		return "", errors.New("controller health unreadable: HOLD")
	}
	defer res.Body.Close()
	var h struct {
		Version string `json:"version"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&h) != nil || !valid(h.Version) {
		return "", errors.New("installed version unreadable: HOLD")
	}
	return h.Version, nil
}

// SaveResult records the worker's JSON report of version atomically.
func SaveResult(version string, raw []byte) error {
	if err := os.MkdirAll(ResultDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(ResultDir, ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(ResultDir, version+".json")); err != nil {
		return err
	}
	dir, err := os.Open(ResultDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Result returns the report of version written at or after since, so an
// older attempt's report never answers a new call.
func Result(version string, since time.Time) ([]byte, bool) {
	path := filepath.Join(ResultDir, version+".json")
	info, err := os.Stat(path)
	if err != nil || info.ModTime().Before(since.Add(-time.Second)) || info.Size() > 16<<10 {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || !json.Valid(raw) {
		return nil, false
	}
	return raw, true
}

// WorkerResult is what the root release worker reports.
type WorkerResult struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Controller  string `json:"controller_sha256"`
	Broker      string `json:"broker_sha256"`
	TestKind    string `json:"test_evidence"`
	OperationID string `json:"operation_id"`
	Unit        string `json:"unit"`
	State       string `json:"state"`
	Origin      string `json:"origin,omitempty"`
}
