package executor

import "mcpshka/internal/sandboxbind"

// Runtime paths live in internal/sandboxbind (0.19.0).
type RuntimeBind = sandboxbind.Bind

const RuntimeAllowlist = sandboxbind.Allowlist
