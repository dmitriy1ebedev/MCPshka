package rootbroker

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestTaskInlineContentPinsBytesAndBoundsChunks(t *testing.T) {
	content := bytes.Repeat([]byte("a"), 10240)
	encoded := base64.StdEncoding.EncodeToString(content)
	var parts []string
	for len(encoded) > 0 {
		n := min(len(encoded), 2800)
		parts = append(parts, encoded[:n])
		encoded = encoded[n:]
	}
	got, err := decodeTaskContent(parts, shaHex(content))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("inline content changed: %v", err)
	}
	for _, parts := range [][]string{nil, {"%%%"}, {strings.Repeat("a", 3001)}, make([]string, 21)} {
		if _, err := decodeTaskContent(parts, shaHex(content)); err == nil {
			t.Fatal("unbounded or malformed content accepted")
		}
	}
	if _, err := decodeTaskContent([]string{base64.StdEncoding.EncodeToString([]byte("changed"))}, shaHex(content)); err == nil {
		t.Fatal("changed bytes accepted under the prepared SHA")
	}
}

func TestAdministrativeTaskRejectsMissingOrChangedInlineContentBeforeStateAccess(t *testing.T) {
	args := []string{"authorize-task", strings.Repeat("a", 32), "owner", strings.Repeat("b", 32), shaHex([]byte("live")), shaHex([]byte("staged"))}
	if _, err := authorizeAdminTask(args, time.Now()); err == nil || !strings.Contains(err.Error(), "inline") {
		t.Fatalf("legacy private staging read still required: %v", err)
	}
	args = append(args, base64.StdEncoding.EncodeToString([]byte("changed")))
	if _, err := authorizeAdminTask(args, time.Now()); err == nil || !strings.Contains(err.Error(), "inline") {
		t.Fatalf("changed inline content reached root state: %v", err)
	}
}
