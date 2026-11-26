package releasecleanup

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUpgradeJournalMetadataStaysOutOfJSON(t *testing.T) {
	u := Upgrade{Version: "0.13.5", State: "SUCCEEDED", At: time.Now()}
	raw, err := json.Marshal(u)
	if err != nil || strings.Contains(string(raw), "SUCCEEDED") || strings.Contains(string(raw), `"At"`) {
		t.Fatalf("journal metadata escaped into release history: %s: %v", raw, err)
	}
	var decoded Upgrade
	if err := json.Unmarshal([]byte(`{"version":"0.13.5","State":"SUCCEEDED","At":"2026-11-17T00:00:00Z"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != u.Version || decoded.State != "" || !decoded.At.IsZero() {
		t.Fatalf("untrusted metadata accepted: %+v", decoded)
	}
}
