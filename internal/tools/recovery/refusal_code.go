package recovery

import "regexp"

var (
	refusalFaceID  = regexp.MustCompile(`(?i)face id|webauthn|passkey|admin_session`)
	refusalSSHAuth = regexp.MustCompile(`(?i)\bssh\b.*(?:denied|auth)|publickey|host key verification`)
)

// clientRefusalCode gives MCP-039's typed code and whether the call reached
// MCPshka. Only a reported server receipt proves dispatch; without it the
// refusal stays on the client side and nothing may be retried as if the
// server had refused.
func ClientRefusalCode(class, text, receipt string) (code, dispatched string) {
	dispatched = "not_observed"
	if receiptID.MatchString(text) {
		dispatched = "receipt_reported"
	}
	switch class {
	case "tool_safety":
		code = "CLIENT_TOOL_SAFETY_BLOCK"
	case "schema_mismatch":
		code = "CLIENT_SCHEMA_STALE"
	case "policy":
		code = "MCP_POLICY_DENIED"
		if refusalFaceID.MatchString(text) {
			code = "FACE_ID_REQUIRED"
		}
	case "auth":
		code = "CLIENT_AUTH_REQUIRED"
		if refusalSSHAuth.MatchString(text) {
			code = "SSH_AUTH_FAILED"
		}
	case "transport":
		code = "NETWORK_DOWN"
	default:
		code = "UNKNOWN_REFUSAL"
	}
	return code, dispatched
}
