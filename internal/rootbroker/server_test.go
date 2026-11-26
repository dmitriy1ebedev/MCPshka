package rootbroker

import (
	"context"
	"errors"
	"mcpshka/internal/brokerwire"
	"testing"
)

func TestBrokerRunReasonSeparatesSQLiteAndHandlerTimeout(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("sqlite: database or disk is full (13)"), "sqlite_full"},
		{errors.New("sqlite: database is locked (5)"), "sqlite_busy"},
		{context.DeadlineExceeded, "handler_deadline"},
		{errors.New("request denied"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := brokerwire.RunReason(context.Background(), tc.err); got != tc.want {
			t.Errorf("brokerwire.RunReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
