package agentlogin

import "errors"

// RootOwnedTool checks the agent CLI is root-owned and not writable; the
// broker wires its own check, unwired it refuses.
var RootOwnedTool = func(string) error { return errors.New("agentlogin: tool check not wired") }
