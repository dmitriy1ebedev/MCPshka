package healthlayer

import "testing"

func TestCatalogHealthSeparatesTransportFromUsableCapabilities(t *testing.T) {
	count := 257
	no, yes := false, true
	v := CatalogHealth{CapabilityManifestHash: "manifest", ExposedCount: &count, IndexRevision: "r1", IndexStaleness: "current", SchemaMismatch: &no, MissingActions: []string{}, ClientRefreshNeeded: &no}
	if v.Evaluate().State != "ok" {
		t.Fatal("fully observed catalog unavailable")
	}
	v.IndexStaleness = "stale"
	if v.Evaluate().State != "stale" {
		t.Fatal("stale index hidden")
	}
	v.IndexStaleness = "current"
	v.SchemaMismatch = &yes
	if v.Evaluate().State != "degraded" {
		t.Fatal("schema mismatch hidden by transport")
	}
	v.SchemaMismatch = nil
	if v.Evaluate().State != "unknown" {
		t.Fatal("unknown schema treated as ready")
	}
	v.SchemaMismatch = &no
	v.MissingActions = []string{"backup_verify"}
	if v.Evaluate().State != "degraded" {
		t.Fatal("missing action hidden")
	}
}

func TestCriticalContainerAllowlist(t *testing.T) {
	for _, name := range []string{"homeassistant", "zigbee2mqtt", "mosquitto"} {
		if !CriticalContainer(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"test", "lane", "lampac", "homeassistant-copy", ""} {
		if CriticalContainer(name) {
			t.Fatal("unallowlisted inspection", name)
		}
	}
}
