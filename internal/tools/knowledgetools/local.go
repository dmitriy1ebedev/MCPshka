package knowledgetools

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/tooldef"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
)

type KnowledgeChatInput struct {
	ArtifactID     string `json:"artifact_id"`
	Format         string `json:"format" jsonschema:"chatgpt, claude, markdown, chatgpt_zip or claude_zip"`
	IdempotencyKey string `json:"idempotency_key"`
}
type KnowledgeChatResult struct {
	Trust string   `json:"trust"`
	IDs   []string `json:"ids"`
	State string   `json:"state"`
}
type KnowledgeLocalInput struct {
	ID       string `json:"id"`
	History  bool   `json:"history,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
}

func RedactLocal(r knowledge.Record, history bool) knowledge.Record {
	r.Source = knowledge.MaskSecrets(r.Source)
	r.Provenance = knowledge.MaskSecrets(r.Provenance)
	r.ID = knowledge.MaskSecrets(r.ID)
	r.Scope = knowledge.MaskSecrets(r.Scope)
	r.Kind = knowledge.MaskSecrets(r.Kind)
	r.Confidence = knowledge.MaskSecrets(r.Confidence)
	r.Freshness = knowledge.MaskSecrets(r.Freshness)
	r.SupersededBy = knowledge.MaskSecrets(r.SupersededBy)
	for i, ref := range r.EvidenceRefs {
		r.EvidenceRefs[i] = knowledge.MaskSecrets(ref)
	}
	fields := map[string]knowledge.Field{}
	for k, f := range r.Fields {

		if !f.Secret && !knowledge.SensitiveField(k) {
			f.Value = knowledge.MaskSecrets(f.Value)
			fields[knowledge.MaskSecrets(k)] = f
		}
	}
	r.Fields = fields
	if !history {
		r.History = nil
	} else {
		for i, h := range r.History {
			r.History[i].Reason = knowledge.MaskSecrets(h.Reason)
			if h.Before != nil {
				var prior knowledge.Record
				if json.Unmarshal(h.Before, &prior) == nil {
					b := RedactLocal(prior, false)
					r.History[i].Before, _ = json.Marshal(b)
				} else {
					r.History[i].Before = nil
				}
			}
		}
	}
	return r
}
func importChat(ctx context.Context, m *jobs.Manager, d LocalDeps, in KnowledgeChatInput) (KnowledgeChatResult, error) {
	out := KnowledgeChatResult{Trust: "UNTRUSTED", State: "imported_private"}
	if _, e := m.Policy().DecideCommand("mcpshka-knowledge-write", nil); e != nil {
		return out, e
	}
	u, e := d.Inbox.Status(d.Owner(ctx), in.ArtifactID)
	if e != nil {
		return out, e
	}
	if u.State != "ready" {
		return out, errors.New("ready owned artifact required")
	}
	f, e := os.Open(u.Path)
	if e != nil {
		return out, e
	}
	defer f.Close()
	s, e := knowledge.FromDatabase(m.Database())
	if e != nil {
		return out, e
	}
	switch in.Format {
	case "chatgpt_zip":
		out.IDs, e = s.ImportChatZIP(f, u.Size, "chatgpt", "artifact:"+u.ID, u.SHA256)
	case "claude_zip":
		out.IDs, e = s.ImportChatZIP(f, u.Size, "claude", "artifact:"+u.ID, u.SHA256)
	default:
		out.IDs, e = s.ImportChat(f, in.Format, "artifact:"+u.ID, u.SHA256)
	}
	if e == nil {
		m.InvalidateEphemeralGroup("knowledge")
	}
	return out, e
}
func RegisterLocal(add func(tooldef.Def), m *jobs.Manager, d LocalDeps) {
	add(tooldef.New(&mcp.Tool{Name: "knowledge_import_chat", Description: "Import a ready owned artifact-inbox ChatGPT conversations.json/zip, Claude conversations.json/zip or Markdown. Bounded streaming, content deduplication, SHA-256 provenance, secret masking. Original remains unchanged; records are private until explicit owner export opt-in. No automatic model extraction or Google Drive publication.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in KnowledgeChatInput) (*mcp.CallToolResult, KnowledgeChatResult, error) {
		out, e := importChat(ctx, m, d, in)
		return nil, out, e
	}))
	add(tooldef.New(&mcp.Tool{Name: "knowledge_local_read", Description: "Owner-local article inspection including private imported articles and optional revision history. Secrets are masked. Separate permission from the external-provider knowledge_read projection; never use this tool to export to a provider.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in KnowledgeLocalInput) (*mcp.CallToolResult, knowledge.Record, error) {
		s, e := knowledge.FromDatabase(m.Database())
		if e != nil {
			return nil, knowledge.Record{}, e
		}
		out, e := s.Inspect(in.ID)
		if e != nil {
			return nil, out, e
		}
		if in.Revision != 0 && in.Revision != out.Revision {
			found := false
			for _, h := range out.History {
				var previous knowledge.Record
				if len(h.Before) > 0 && json.Unmarshal(h.Before, &previous) == nil && previous.Revision == in.Revision {
					out = previous
					found = true
					break
				}
			}
			if !found {
				return nil, knowledge.Record{}, knowledge.ErrMissing
			}
		}
		out = RedactLocal(out, in.History)
		raw, _ := json.Marshal(out)
		if len(raw) > 65536 {
			return nil, knowledge.Record{}, errors.New("history exceeds 64 KiB; request a specific revision")
		}
		return nil, out, nil
	}))
}

// ArticlePublisher is the next-slice seam; SQLite revisions remain authoritative.
// Implementations must reject revision conflicts and publish only Visible fields.
type ArticlePublisher interface {
	Publish(context.Context, knowledge.Record, uint64) (documentID string, err error)
}

type LocalDeps struct {
	Inbox *artifacts.Inbox
	Owner func(context.Context) string
}
