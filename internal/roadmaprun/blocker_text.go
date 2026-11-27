package roadmaprun

import (
	"regexp"
	"strconv"
)

// opaqueToken matches long base64 runs (longer than any SHA-256 or ID): an encoded payload copied into a
// stop reason made roadmap_run_status unreadable for the owner (MCP-102).
var opaqueToken = regexp.MustCompile(`[A-Za-z0-9+/_=-]{100,}`)

// readableBlocker keeps the human cause and replaces opaque payloads with a
// short marker; the full payload stays in the agent's own journal.
func readableBlocker(reason string) string {
	return opaqueToken.ReplaceAllStringFunc(reason, func(tok string) string {
		return "<encoded payload " + strconv.Itoa(len(tok)) + " bytes omitted; see the run journal>"
	})
}
