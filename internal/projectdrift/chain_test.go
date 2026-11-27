package projectdrift

import (
	"context"
	"encoding/json"
	"mcpshka/internal/releasequeue"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceChainNeverMixesCandidateWithInstalled(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	head := strings.Repeat("a", 40)
	q := releasequeue.Item{Version: "0.12.9", Commit: head, Stage: "BUILD", BuildPermit: "build", SourceDigest: "digest", PreviousVersion: "0.12.8", UpdatedAt: now}
	raw, _ := json.Marshal(q)
	if err = db.Put(releasequeue.Prefix+q.Version+".json", raw); err != nil {
		t.Fatal(err)
	}
	c := SourceChain(context.Background(), db, releasequeue.Repo, head, "test", true, now)
	if c.CI.Identity != head || c.Integration.State != "RECORDED" || c.Candidate.Identity != "digest" || c.Installed.State != "UNKNOWN" || c.Rollback.State != "UNKNOWN" {
		t.Fatal(c)
	}
	c = SourceChain(context.Background(), db, "/other", head, "", false, now)
	if c.Candidate.State != "UNKNOWN" || c.CI.State != "UNKNOWN" {
		t.Fatal(c)
	}
}
