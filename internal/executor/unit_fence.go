package executor

import "mcpshka/internal/unitfence"

// The systemd stop-and-verify fence lives in internal/unitfence (0.19.0).
type SystemdFence = unitfence.Fence
