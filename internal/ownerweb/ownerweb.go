// Package ownerweb is the owner's browser side of MCPshka: the Connections
// page (providers, AI «brains», clouds, Lane), the OAuth connector that lets
// ChatGPT/Claude reach /mcp, the Face ID (passkey) portal and cloud storage
// sign-in. It serves HTTP only; MCP tools stay in mcpserver, which wires the
// few callbacks below at start (08.11: mcpserver split into modules).
package ownerweb

import (
	"context"
	"net/http"
)

// Callbacks into the tool server, set once by mcpserver before serving.
var (
	// ContinueApproval resumes the approved task a finished Face ID unlocked.
	ContinueApproval func(ctx context.Context, finish []byte)
	// IssuesAPI and IssuesPage serve the issue registry on the Connections page.
	IssuesAPI  func(ctx context.Context, w http.ResponseWriter, r *http.Request, rest string)
	IssuesPage func(w http.ResponseWriter, r *http.Request)
	// MemoryDir is the state directory holding memory.db («Память» page).
	MemoryDir string
)
