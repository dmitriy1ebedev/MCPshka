package notify

import (
	"context"
	"encoding/json"
	"mcpshka/internal/statestore"
	"path/filepath"
	"testing"
	"time"
)

func TestDeliveryLegacySentNeverReplays(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = Record(db, Event{Key: "legacy", Severity: "INFO"}, false, now); err != nil {
		t.Fatal(err)
	}
	raw, err := db.Get(eventKey("legacy"))
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	e.Delivery = &Delivery{State: "delivered"}
	raw, _ = json.Marshal(e)
	if err = db.Put(eventKey("legacy"), raw); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{state: "delivered"}
	if err = Deliver(context.Background(), db, "legacy", sender, now); err != nil || sender.calls != 0 {
		t.Fatal(sender, err)
	}
}
