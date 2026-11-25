package pluginops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mcpshka/internal/mcphub"
)

func TestCallNeedsDottedNameAndLiveFaceID(t *testing.T) {
	d := Deps{Hub: mcphub.New(t.TempDir()), RequireAdmin: func(context.Context, string) error { return errors.New("Face ID session not active") }}
	if _, err := Call(context.Background(), d, CallInput{Name: "context7"}, ""); err == nil || !strings.Contains(err.Error(), "<server>.<tool>") {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), d, CallInput{Name: "context7.query", AdminSessionID: "dead"}, ""); err == nil || !strings.Contains(err.Error(), "Face ID") {
		t.Fatal("a dead Face ID session must refuse before any outgoing call:", err)
	}
	if _, err := Call(context.Background(), d, CallInput{Name: "nope.query"}, ""); err == nil {
		t.Fatal("unknown server must fail")
	}
	if _, err := Call(context.Background(), Deps{}, CallInput{Name: "a.b"}, ""); err == nil {
		t.Fatal("no hub must fail")
	}
}

func TestFindEmptyHubPointsToPage(t *testing.T) {
	out := Find(context.Background(), mcphub.New(t.TempDir()), ToolsInput{Query: "docs"})
	if len(out.Servers) != 0 || len(out.Tools) != 0 || !strings.Contains(out.Next, page) {
		t.Fatalf("%+v", out)
	}
	if !matchAll("context7 resolve-library-id docs", []string{"docs", "resolve"}) || matchAll("x", []string{"y"}) {
		t.Fatal("matchAll")
	}
}
