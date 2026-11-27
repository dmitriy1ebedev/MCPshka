package notify

import (
	"context"
	"mcpshka/internal/statestore"
	"path/filepath"
	"testing"
	"time"
)

type fakeSender struct {
	calls int
	state string
}

func (f *fakeSender) Send(context.Context, Event, string) (string, error) {
	f.calls++
	return f.state, nil
}
func TestDeliveryRetryCancelAndUnknown(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := ObserveChallenges(db, []Challenge{{"one", true, now}, {"two", true, now}}, now); err != nil {
		t.Fatal(err)
	}
	if err := ObserveChallenges(db, []Challenge{{"one", true, now}}, now); err != nil {
		t.Fatal(err)
	}
	items, err := Open(context.Background(), db)
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	sender := &fakeSender{state: "not_delivered"}
	if err := Deliver(context.Background(), db, "face-id/one", sender, now); err != nil {
		t.Fatal(err)
	}
	if err := Deliver(context.Background(), db, "face-id/one", sender, now); err != nil || sender.calls != 1 {
		t.Fatal(sender, err)
	}
	if err := ObserveChallenges(db, []Challenge{{"one", false, now}}, now); err != nil {
		t.Fatal(err)
	}
	if err := Deliver(context.Background(), db, "face-id/one", sender, now.Add(time.Hour)); err != nil || sender.calls != 1 {
		t.Fatal(sender, err)
	}
	sender.state = "UNKNOWN"
	if err := Deliver(context.Background(), db, "face-id/two", sender, now); err != nil {
		t.Fatal(err)
	}
	if err := Deliver(context.Background(), db, "face-id/two", sender, now.Add(time.Hour)); err != nil || sender.calls != 2 {
		t.Fatal(sender, err)
	}
}
