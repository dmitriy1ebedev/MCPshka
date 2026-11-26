package native

import _ "embed"

// ReleaseGate pins the existing validator to the broker image across helper upgrades.
//
//go:embed release-gate.py
var ReleaseGate string
