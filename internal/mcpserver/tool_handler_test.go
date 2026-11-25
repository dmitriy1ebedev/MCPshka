package mcpserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"testing"
)

type handlerOutput struct {
	Value string `json:"value"`
}

func TestMutationAdapterPreservesTypedOutputAndReplay(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "handler_error"}[fail], func(t *testing.T) {
			r := &toolRegistry{
				server: mcp.NewServer(&mcp.Implementation{Name: "typed-handler", Version: "1"}, nil),
				store:  idempotency.New(t.TempDir()),
				readers: map[string]probeHandler{
					"service_status": func(context.Context, map[string]any) (any, error) {
						return map[string]any{"active_state": "active"}, nil
					},
				},
				tools: map[string]bool{},
			}
			calls := 0
			want := &handlerOutput{Value: "kept"}
			sentinel := errors.New("handler stopped")
			addTool(r, &mcp.Tool{Name: "service_action"}, func(context.Context, *mcp.CallToolRequest, ServiceActionInput) (*mcp.CallToolResult, *handlerOutput, error) {
				calls++
				if fail {
					return nil, want, sentinel
				}
				return nil, want, nil
			})
			args := map[string]any{"service": "test.service", "action": "restart", "idempotency_key": "typed"}
			for attempt := 0; attempt < 2; attempt++ {
				_, value, err := r.invokers["service_action"](context.Background(), args)
				if fail && attempt == 1 {
					if err == nil || calls != 1 {
						t.Fatalf("failed effect replayed: calls=%d err=%v", calls, err)
					}
					continue
				}
				got, ok := value.(*handlerOutput)
				if !ok || got == nil || got.Value != want.Value {
					t.Fatalf("typed output lost: %#v", value)
				}
				if fail {
					if !errors.Is(err, sentinel) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}
