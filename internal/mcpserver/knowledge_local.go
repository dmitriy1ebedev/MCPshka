package mcpserver

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/knowledgetools"
)

type KnowledgeChatInput = knowledgetools.KnowledgeChatInput
type KnowledgeChatResult = knowledgetools.KnowledgeChatResult
type KnowledgeLocalInput = knowledgetools.KnowledgeLocalInput
type ArticlePublisher = knowledgetools.ArticlePublisher

var redactLocal = knowledgetools.RedactLocal

func registerKnowledgeLocal(r *toolRegistry, m *jobs.Manager) {
	knowledgetools.RegisterLocal(r.install, m, knowledgetools.LocalDeps{Inbox: artifactInbox(m), Owner: uploadOwner})
}
