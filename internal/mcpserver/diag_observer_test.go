package mcpserver

import (
	"context"
	"strings"
	"testing"
)

func TestDIAGObserverViews(t *testing.T) {
	var got []string
	broker := func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte(`{"state":"OK"}`), nil
	}
	out, err := diagObserver(context.Background(), DIAGObserverInput{}, broker)
	if err != nil || out["view"] != "status" || len(got) != 0 {
		t.Fatalf("%v %v %v", out, err, got)
	}
	out, err = diagObserver(context.Background(), DIAGObserverInput{View: "incidents", Window: "30m"}, broker)
	if err != nil || strings.Join(got, " ") != "incidents 30m" || out["window"] != "30m" {
		t.Fatalf("%v %v %v", out, err, got)
	}
	for _, in := range []DIAGObserverInput{{View: "shell"}, {View: "baseline", Window: "45m; rm"}} {
		if _, err := diagObserver(context.Background(), in, broker); err == nil {
			t.Fatalf("%+v must be refused", in)
		}
	}
}
