// Package selfheal holds what one automatic root maintenance pass did; the
// broker runs it, the controller reads it (S5.LEAN: kept out of rootbroker).
package selfheal

import "mcpshka/internal/projectlease"

// Result is one automatic pass: stale executor permits fenced, failed units
// reset and (S5.LEASE) dead holders' leases released with nothing to lose.
type Result struct {
	Reconciled  int                          `json:"reconciled"`
	Skipped     int                          `json:"skipped"`
	ResetFailed int                          `json:"reset_failed"`
	Leases      projectlease.AutoStaleResult `json:"leases"`
}
