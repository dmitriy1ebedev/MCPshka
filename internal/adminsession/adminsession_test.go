package adminsession

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRequire(t *testing.T) {
	owner := func(context.Context) (string, error) { return "o", nil }
	state := func(s string) func(context.Context, string, string) (string, error) {
		return func(context.Context, string, string) (string, error) { return s, nil }
	}
	ctx := context.Background()
	for _, id := range []string{"", "  "} {
		if err := Require(ctx, id, owner, state("active")); err == nil || !strings.Contains(err.Error(), "admin_session_id required") {
			t.Fatalf("%q: %v", id, err)
		}
	}
	if err := Require(ctx, "s", owner, state("expired")); err == nil {
		t.Fatal("inactive window must refuse")
	}
	if err := Require(ctx, "s", func(context.Context) (string, error) { return "", errors.New("no owner") }, state("active")); err == nil {
		t.Fatal("unknown owner must refuse")
	}
	if err := Require(ctx, "s", owner, state("active")); err != nil {
		t.Fatal(err)
	}
}
