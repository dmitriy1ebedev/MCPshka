package rootbroker

import (
	"reflect"
	"testing"
	"time"
)

func TestStaleAdminUnitsRespectRetention(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	old := "mcpshka-integration-admin-0123456789abcdef0123456789abcdef.service"
	fresh := "mcpshka-integration-admin-fedcba9876543210fedcba9876543210.service"
	got := staleAdminUnits(map[string]time.Time{
		old:                   now.Add(-2 * time.Hour),
		fresh:                 now.Add(-10 * time.Minute),
		"zigbee2mqtt.service": now.Add(-48 * time.Hour),
		"mcpshka-integration-admin-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service": {},
	}, now)
	if !reflect.DeepEqual(got, []string{old}) {
		t.Fatalf("got %v", got)
	}
}
