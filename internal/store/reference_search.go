package store

// ReferenceSearchFields is a fixed whitelist: caller input never becomes SQL.
func ReferenceSearchFields(collection string) []string {
	switch collection {
	case "users":
		return []string{"name", "username", "email"}
	case "teams", "schedules", "escalation_chains", "integrations", "maintenance_windows":
		return []string{"name"}
	default:
		return nil
	}
}
