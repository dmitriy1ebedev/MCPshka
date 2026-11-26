// Package memorytools exposes the owner's memory (internal/memory) as MCP
// tools; mcpserver only installs them (S5.LEAN: new tools live in their own
// package).
package memorytools

import (
	"context"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/memory"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tooldef"
)

var nightRunning atomic.Bool

// Register installs memory_write and memory_recall over the store in
// stateDir; night starts the nightly consolidation once per process;
// requireAdmin admits optimize only under the owner's Face ID window.
func Register(add func(tooldef.Def), stateDir string, night func(), requireAdmin func(context.Context, string) error, db *statestore.DB) {
	registerArchive(add, stateDir, db)
	open := func() (*memory.Store, error) { return memory.Open(stateDir) }
	if night != nil && nightRunning.CompareAndSwap(false, true) {
		go night()
	}
	add(tooldef.New(&mcp.Tool{Name: "memory_write", Description: "«Запиши в базу …»: one distilled record (decision, lesson, problem, fact, preference, note); «напомни …» is kind=reminder with remind_at. Give questions: 5–10 ways the owner would ask for it. The same title with a new body supersedes the old one and keeps its history; question_sets only adds questions to existing records by source; «забудь …» is forget=<id> (leaves recall, stays in history; erase=true removes it with its index). Requires retry key." + " optimize=<plan_sha> applies a memory_recall audit=true plan (stale sections → archive, duplicates/older releases superseded; nothing deleted), optimize_undo=<op> restores it; both need admin_session_id.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, _ *mcp.CallToolRequest, in memory.WriteInput) (*mcp.CallToolResult, memory.WriteResult, error) {
		if in.Optimize != "" || in.OptimizeUndo != "" {
			if err := requireAdmin(ctx, in.AdminSessionID); err != nil {
				return nil, memory.WriteResult{}, err
			}
		}
		out, err := memory.WriteWith(ctx, open, in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "memory_recall", Description: "«Вспомни», «поищи в базе знаний», «мы уже находили/решали», «как в прошлый раз» — call this before answering: best records for plain words from the whole memory (own records and knowledge) with source, date and why ranked; as_of answers what was true then; id inspects one record with its questions; kind and tag narrow the answer (kind=problem — incidents, kind=preference — how the owner works, tag — a device or artifact); due=true lists reminders whose time has come; without_questions lists records still lacking recall questions; inbox=true gives the next chat-archive chunks to distil. Words (BM25) and meaning (word vectors) fused; no model or network call." + " audit=true: read-only memory audit with the optimize plan and the state control set (fresh@3).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in memory.Query) (*mcp.CallToolResult, memory.RecallResult, error) {
		out, err := memory.RecallWith(ctx, open, in)
		return nil, out, err
	}))
}
