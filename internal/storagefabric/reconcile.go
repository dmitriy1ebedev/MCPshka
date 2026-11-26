package storagefabric

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// An operation whose outcome is not known used to fence every later storage
// mutation until somebody repaired the journal by hand (a mkdir throttled on
// 09.11 blocked uploads for three days). The outcome is observable: read the
// cloud back and settle the operation as done or failed. Only a read that
// itself fails leaves it UNKNOWN.

// presence is what a read-back saw at one remote path.
type presence int

const (
	unseen presence = iota // the read failed: nothing is known
	absent
	present
)

// verdict settles one plain operation from what the cloud shows now.
// size is the size of the object at src (upload) when present.
func verdict(r Request, src, dst presence, size int64) string {
	switch r.Action {
	case "download": // no remote effect; the local file is rewritten on retry
		return "failed"
	case "mkdir":
		return map[presence]string{present: "done", absent: "failed"}[src]
	case "upload":
		if src == present && size == r.ArtifactSize {
			return "done"
		}
		if src == absent {
			return "failed"
		}
	case "move", "rename":
		if src == absent && dst == present {
			return "done"
		}
		if src == present && dst == absent {
			return "failed"
		}
	case "trash":
		return map[presence]string{absent: "done", present: "failed"}[src]
	}
	return ""
}

// look reads one remote path back.
func (e Engine) look(ctx context.Context, provider, p string) (presence, int64) {
	remote, err := Remote(provider, p, false)
	if err != nil {
		return unseen, 0
	}
	raw, err := e.rclone(ctx, provider, "lsjson", remote, "--stat")
	if err != nil {
		if strings.HasPrefix(reasonOf(err), "NOT_FOUND") {
			return absent, 0
		}
		return unseen, 0
	}
	var item *struct{ Size int64 }
	if json.Unmarshal(raw, &item) != nil {
		return unseen, 0
	}
	if item == nil {
		return absent, 0
	}
	return present, item.Size
}

// settle turns an operation without a known outcome into done or failed when
// its worker is gone and the cloud can be read. It reports whether the
// operation no longer fences new work.
func (e Engine) settle(ctx context.Context, op *operation) bool {
	switch op.Result.State {
	case "pending", "running", "UNKNOWN":
	default:
		return true
	}
	if op.Request.Action == "backup_create" || op.Request.Action == "backup_verify" {
		return false // the backup workflow resumes from its own checkpoints
	}
	// A worker that was admitted a moment ago may not be visible to systemd yet.
	if info, err := os.Stat(e.opPath(op.Result.ID)); err != nil || time.Since(info.ModTime()) < 30*time.Second {
		return false
	}
	raw, err := command(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", "mcpshka-storage-"+op.Result.ID+".service")
	if state := strings.TrimSpace(string(raw)); err != nil || state == "active" || state == "activating" {
		return false
	}
	src, size := e.look(ctx, op.Request.Provider, op.Request.Path)
	dst := unseen
	if op.Request.Destination != "" {
		dst, _ = e.look(ctx, op.Request.Provider, op.Request.Destination)
	}
	switch verdict(op.Request, src, dst, size) {
	case "done":
		op.Result.State, op.Result.Message = "done", "outcome was uncertain; confirmed by reading the cloud back"
	case "failed":
		op.Result.State, op.Result.Message = "failed", "outcome was uncertain; the cloud shows no effect — repeat with a new idempotency_key"
	default:
		op.Result.State = "UNKNOWN"
		if !strings.Contains(op.Result.Message, "could not be read back") {
			op.Result.Message = strings.TrimSpace(op.Result.Message + " The cloud could not be read back yet: call storage_transfer_status again.")
		}
		_ = e.save(*op)
		return false
	}
	return e.save(*op) == nil
}
