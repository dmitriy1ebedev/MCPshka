package cloud

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/cloudtasks"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tooldef"
	"os"
)

// Receipt reads never query a provider or grant deployment authority.
type CloudTaskStatusInput struct {
	ID string `json:"id"`
}
type CloudTaskListInput struct {
	Limit int `json:"limit,omitempty"`
}
type CloudTaskResult struct {
	Found            bool                        `json:"found"`
	Receipt          *cloudtasks.Receipt         `json:"receipt,omitempty"`
	Dispatch         *cloudtasks.DispatchReceipt `json:"dispatch,omitempty"`
	DispatchEnabled  bool                        `json:"dispatch_enabled"`
	DeployAuthorized bool                        `json:"deploy_authorized"`
}
type CloudTaskListResult struct {
	Tasks            []cloudtasks.Receipt         `json:"tasks"`
	Dispatches       []cloudtasks.DispatchReceipt `json:"dispatches"`
	DispatchEnabled  bool                         `json:"dispatch_enabled"`
	DeployAuthorized bool                         `json:"deploy_authorized"`
}

func RegisterTaskStatus(add func(tooldef.Def), db *statestore.DB) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "cloud_task_status", Description: "Read a durable SQLite cloud dispatch or legacy observation receipt. UNTRUSTED; no provider request, worker launch, lease or deploy authority.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CloudTaskStatusInput) (*mcp.CallToolResult, CloudTaskResult, error) {
			j, e := cloudtasks.New(db)
			if e != nil {
				return nil, CloudTaskResult{}, e
			}
			dispatch, e := j.Dispatch(in.ID)
			if e == nil {
				return nil, CloudTaskResult{Found: true, Dispatch: &dispatch}, nil
			}
			if !errors.Is(e, os.ErrNotExist) {
				return nil, CloudTaskResult{}, e
			}
			rec, e := j.Get(in.ID)
			if errors.Is(e, os.ErrNotExist) {
				return nil, CloudTaskResult{Found: false}, nil
			}
			if e != nil {
				return nil, CloudTaskResult{}, e
			}
			return nil, CloudTaskResult{Found: true, Receipt: &rec}, nil
		}))
	add(tooldef.New(&mcp.Tool{Name: "cloud_task_list", Description: "Read bounded durable cloud task receipts, without prompts/credentials and without invoking any provider. Observations remain UNTRUSTED.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CloudTaskListInput) (*mcp.CallToolResult, CloudTaskListResult, error) {
			j, e := cloudtasks.New(db)
			if e != nil {
				return nil, CloudTaskListResult{}, e
			}
			if in.Limit == 0 {
				in.Limit = 10
			}
			tasks, e := j.List(in.Limit)
			if e != nil {
				return nil, CloudTaskListResult{}, e
			}
			dispatches, e := j.DispatchList(in.Limit)
			return nil, CloudTaskListResult{Tasks: tasks, Dispatches: dispatches}, e
		}))
}
