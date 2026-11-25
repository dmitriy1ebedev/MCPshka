package evidence

// Verdict summarizes gate states for a product (MCP-088): DONE only when every
// required gate passed on the current inputs; a missing release/delivery
// gate is NOT_RELEASED; a failure is FAILED; anything else (missing, stale
// evidence) is BLOCKED. HTTP readiness or a passing build alone is never DONE.
func Verdict(states []GateState) string {
	if len(states) == 0 {
		return "BLOCKED"
	}
	verdict := "DONE"
	for _, s := range states {
		switch {
		case s.State == "FAIL":
			return "FAILED"
		case s.State == "PASS":
		case s.Gate == "release" || s.Gate == "delivery":
			if verdict == "DONE" {
				verdict = "NOT_RELEASED"
			}
		default:
			verdict = "BLOCKED"
		}
	}
	return verdict
}
