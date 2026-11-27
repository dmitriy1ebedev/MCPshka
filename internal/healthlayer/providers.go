package healthlayer

type CatalogHealth struct {
	State                  string   `json:"state"`
	CapabilityManifestHash string   `json:"capability_manifest_hash"`
	ExposedCount           *int     `json:"exposed_count"`
	IndexRevision          string   `json:"index_revision"`
	IndexStaleness         string   `json:"index_staleness"`
	SchemaMismatch         *bool    `json:"schema_mismatch"`
	MissingActions         []string `json:"missing_actions"`
	ClientRefreshNeeded    *bool    `json:"client_refresh_needed"`
	Truncated              bool     `json:"truncated"`
}

func (v CatalogHealth) Evaluate() CatalogHealth {
	v.State = "unknown"
	if v.MissingActions == nil {
		v.MissingActions = []string{}
	}
	if v.CapabilityManifestHash != "" && v.ExposedCount != nil && v.SchemaMismatch != nil && v.ClientRefreshNeeded != nil && v.IndexRevision != "" && v.IndexStaleness == "current" && !v.Truncated {
		v.State = "ok"
	}
	if v.IndexStaleness == "stale" {
		v.State = "stale"
	}
	if len(v.MissingActions) > 0 || v.SchemaMismatch != nil && *v.SchemaMismatch || v.ClientRefreshNeeded != nil && *v.ClientRefreshNeeded {
		v.State = "degraded"
	}
	return v
}

func CriticalContainer(name string) bool {
	switch name {
	case "homeassistant", "zigbee2mqtt", "mosquitto":
		return true
	}
	return false
}
