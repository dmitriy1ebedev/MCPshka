package mcpserver

import (
	"context"
	"errors"
	"mcpshka/internal/failure"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ROADMAP S1: every tool failure carries a class and one deterministic next
// safe action (internal/failure.Classify); mcpserver adds the denial route.

type FailureClass = failure.Class

const (
	FailFencedUnknown    = failure.FencedUnknown
	FailQuotaExhausted   = failure.QuotaExhausted
	FailAuthRequired     = failure.AuthRequired
	FailApprovalRequired = failure.ApprovalRequired
	FailOwnerBusy        = failure.OwnerBusy
	FailPolicyDenied     = failure.PolicyDenied
	FailTransient        = failure.Transient
	FailDisconnected     = failure.Disconnected
	FailVerified         = failure.FailedVerified
	FailDeadline         = failure.Deadline
	FailEvidenceMissing  = failure.EvidenceMissing
)

type classifiedError = failure.Classified

func classifyFailure(err error) error {
	return failure.Classify(err, func(text string) string { return routeForDenial(text).suffix() })
}

var containsMark = failure.ContainsMark

// classifyHandler wraps a tool handler so every returned error is classified.
func classifyHandler[In, Out any](h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {

		started := time.Now()
		res, out, err := h(ctx, req, in)
		if err != nil && failure.Details(err) == nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			var budget time.Duration
			if deadline, ok := ctx.Deadline(); ok {
				budget = max(time.Duration(0), deadline.Sub(started))
			}
			err = failure.Context(ctx, "mcpserver.classifyHandler", budget, err)
		}
		return res, out, classifyFailure(err)
	}
}
