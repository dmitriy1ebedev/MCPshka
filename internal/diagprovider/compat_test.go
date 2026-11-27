package diagprovider

import (
	"errors"
	"testing"
)

func TestRealDIAGCapabilitiesSchema(t *testing.T) {
	c, e := ParseDIAGCapabilities([]byte(`{"schema_version":"diag.capabilities/v1alpha1","deployment":{"mode":"native","host_observer":{"no_new_privileges":true}}}`))
	if e != nil || c.Protocol != 0 || c.Version != DIAGCapabilitiesSchema || !c.ReadOnly {
		t.Fatalf("%+v %v", c, e)
	}
	for _, input := range []string{
		`{"schema_version":"diag.capabilities/v2","deployment":{"mode":"native"}}`,
		`{"schema_version":"diag.capabilities/v1alpha1","deployment":{"mode":"unknown"}}`,
		`{}`, `invalid`,
	} {
		if _, e := ParseDIAGCapabilities([]byte(input)); !errors.Is(e, ErrIncompatible) {
			t.Fatalf("%s: %v", input, e)
		}
	}
	if _, e := ParseDIAGCapabilities(make([]byte, 64<<10+1)); !errors.Is(e, ErrUnsafe) {
		t.Fatal(e)
	}
}
