package diagprovider

import (
	"encoding/json"
	"errors"
	"strings"
)

// DIAGCapabilitiesSchema is the observed schema of the installed DIAG CLI.
// It is deliberately distinct from the internal provider protocol.
const DIAGCapabilitiesSchema = "diag.capabilities/v1alpha1"

// ParseDIAGCapabilities accepts only the actual DIAG CLI capabilities schema.
// A schema match proves only the format, not authorization to launch jobs.
func ParseDIAGCapabilities(data []byte) (Capabilities, error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return Capabilities{}, ErrUnsafe
	}
	var wire struct {
		SchemaVersion string `json:"schema_version"`
		Deployment    struct {
			Mode         string `json:"mode"`
			HostObserver struct {
				NoNewPrivileges bool `json:"no_new_privileges"`
			} `json:"host_observer"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Capabilities{}, errors.Join(ErrIncompatible, err)
	}
	if wire.SchemaVersion != DIAGCapabilitiesSchema || !strings.Contains("|native|docker|", "|"+wire.Deployment.Mode+"|") {
		return Capabilities{}, ErrIncompatible
	}
	// No actual DIAG investigation protocol is exposed by this document.
	// Fail closed: report compatibility metadata but do not grant Start.
	return Capabilities{Protocol: 0, Version: wire.SchemaVersion, Health: "capabilities-readable", ReadOnly: true}, nil
}
