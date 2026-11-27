package failure

// Disposition tells clients whether to reconcile, obtain authority, or wait for
// missing evidence. None of these categories authorizes retrying a mutation.
func Disposition(class Class) string {
	switch class {
	case Transient, Deadline, Disconnected:
		return "transient"
	case AuthRequired, ApprovalRequired:
		return "needs_approval"
	case PolicyDenied:
		return "no_authority"
	default:
		return "blocker"
	}
}
