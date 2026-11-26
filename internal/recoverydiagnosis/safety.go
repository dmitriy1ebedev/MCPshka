package recoverydiagnosis

func prohibited(code string) []string {
	switch code {
	case "AUTH_OK_but_LEASE_BUSY", "legacy_owner_unbound", "historical_deploy_unknown", "broker_incompatible", "PROVIDER_QUOTA_EXHAUSTED":
		return []string{"break_or_delete_lock", "replay_unknown_mutation", "downgrade_security"}
	}
	return nil
}
