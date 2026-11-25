package rootbroker

import (
	"context"
	"strings"
	"testing"
)

func TestDockerLogPageChecksScopeBeforeLaunch(t *testing.T) {
	for _, args := range [][]string{
		{"logs-page"},
		{"logs-page", `{"container":"not-allowlisted"}`},
		{"logs-page", `{"container":"homeassistant","level":"DEBUG"}`},
		{"logs-page", `{"container":"homeassistant","cursor":"broken"}`},
		{"logs-page", strings.Repeat("x", 4097)},
	} {
		data, err := runDockerLogsPage(context.Background(), testConfig(), args)
		if err == nil || len(data) > 0 {
			t.Fatalf("invalid broker query accepted: %v %q %v", args, data, err)
		}
	}
}
