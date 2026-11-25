package toolcontract

import "errors"

// ScriptMutable admits a code_run mcp.mutate call (S5.CM.6) only for a tool
// with a mutation contract (probe, recovery, retry key) whose key is
// present, and returns that key. Approval-only (irreversible) tools and the
// dispatchers stay direct calls.
func ScriptMutable(name string, args map[string]any) (string, error) {
	if name == "code_run" || name == "tool_invoke" || name == "exec_start" {
		return "", errors.New("call it directly")
	}
	c, ok := Contracts[name]
	if !ok {
		return "", errors.New("no mutation contract: read-only tools go through mcp.call, others are called directly")
	}
	if c.Irreversible {
		return "", errors.New("approval-only mutation: call it directly")
	}
	return RetryKey(c, args)
}
