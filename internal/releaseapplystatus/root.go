package releaseapplystatus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mcpshka/internal/releaseapply"
)

var Version = regexp.MustCompile(`^0\.[0-9]{1,4}\.[0-9]{1,6}$`)
var operationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

var AuditDir = "/var/lib/mcpshka-executor/coordinated-upgrades"

// Read only the worker's fixed report and the last immutable event of its operation.
func Root(args []string) ([]byte, error) {
	if len(args) != 1 || !Version.MatchString(args[0]) {
		return nil, errors.New("release apply status requires version")
	}
	version := args[0]
	out := map[string]any{"version": version, "state": "NOT_STARTED"}
	hold := func(reason string) ([]byte, error) {
		out["state"], out["reason"] = "HOLD", reason
		return json.Marshal(out)
	}
	raw, info, err := ReadProtected(filepath.Join(releaseapply.ResultDir, version+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return json.Marshal(out)
	}
	if err != nil {
		return hold("release worker report unreadable: " + err.Error())
	}
	if json.Unmarshal(raw, &out) != nil || out["version"] != version {
		out = map[string]any{"version": version}
		return hold("invalid release worker report")
	}
	out["reported_at"] = info.ModTime().UTC().Format(time.RFC3339Nano)
	if out["state"] == "HOLD" {
		return json.Marshal(out)
	}
	operation, _ := out["operation_id"].(string)
	if !operationID.MatchString(operation) {
		return hold("invalid root operation identity")
	}
	dir := filepath.Join(AuditDir, operation)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) && time.Since(info.ModTime()) < time.Minute {
		return json.Marshal(out) // report sealed before the helper creates its journal
	}
	if err != nil {
		return hold("root audit unavailable: " + err.Error())
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 || len(names) > 128 {
		return hold("unknown or oversized root audit")
	}
	sort.Strings(names)
	raw, _, err = ReadProtected(filepath.Join(dir, names[len(names)-1]))
	var event struct {
		Schema         string              `json:"schema"`
		State          string              `json:"state"`
		Error          string              `json:"error"`
		Traceback      string              `json:"traceback"`
		RollbackErrors []map[string]string `json:"rollback_errors"`
	}
	if err != nil || json.Unmarshal(raw, &event) != nil || event.Schema != "mcpshka.paired-upgrade-audit/v1" {
		return hold("invalid root audit")
	}
	out["last_phase"], out["error"], out["traceback"], out["rollback_errors"] = event.State, event.Error, event.Traceback, event.RollbackErrors
	switch event.State {
	case "SUCCEEDED":
		out["state"] = "SUCCEEDED"
	case "ROLLED_BACK":
		out["state"] = "ROLLED_BACK"
	case "PREPARED", "BACKED_UP", "HELPER_SEALED", "FENCED", "SWAPPING", "APPLY_FAILED", "ROLLBACK_INTENT":
		// A durable intent is progress only while its worker can still be running.
		info, statErr := os.Stat(filepath.Join(dir, names[len(names)-1]))
		if statErr != nil || time.Since(info.ModTime()) > 5*time.Minute {
			return hold("stale nonterminal root audit: " + event.State)
		}
		out["state"] = "APPLYING"
	default:
		return hold("root audit " + event.State + ": " + event.Error)
	}
	return json.Marshal(out)
}
