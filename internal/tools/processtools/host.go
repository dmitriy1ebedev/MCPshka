package processtools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/hostprocess"
	"mcpshka/internal/tooldef"
	"strconv"
)

type PSListInput struct {
	Filter string `json:"filter,omitempty"`
	Sort   string `json:"sort,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type PSKillInput struct {
	PID            int    `json:"pid"`
	StartTicks     uint64 `json:"start_ticks"`
	Signal         int    `json:"signal,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

func RegisterHost(add func(tooldef.Def)) {
	add(tooldef.New(&mcp.Tool{Name: "ps_list", Description: "List up to 100 host processes with pid, ppid, user, lifetime CPU percent, RSS bytes, systemd unit, start_ticks and secret-filtered command (256 runes). Filter by text; sort cpu (default), rss or pid.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in PSListInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := hostprocess.List(in.Filter, in.Sort, in.Limit)
		return nil, map[string]any{"processes": out}, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "ps_kill", Description: "Signal a host process through the root broker using PID and start_ticks from ps_list. Default signal 15; 1..64 accepted. Refuses PID 1, management ancestors and all live service cgroups, including critical daemons. PID reuse is fenced by pidfd.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, req *mcp.CallToolRequest, in PSKillInput) (*mcp.CallToolResult, map[string]any, error) {
		if !brokerclient.Enabled() {
			return nil, nil, errors.New("ps_kill requires privileged root broker")
		}
		if in.Signal == 0 {
			in.Signal = 15
		}
		raw, err := brokerclient.Run(ctx, "process-signal", strconv.Itoa(in.PID), strconv.FormatUint(in.StartTicks, 10), strconv.Itoa(in.Signal))
		if err != nil {
			return nil, nil, err
		}
		var out map[string]any
		err = json.Unmarshal(raw, &out)
		return nil, out, err
	}))
}
