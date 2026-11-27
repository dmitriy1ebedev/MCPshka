package codexcoord

import (
	"encoding/json"
	"strings"
)

// Diagnostic contains fixed classes only; raw daemon/config/error text is excluded.
type Diagnostic struct {
	Stage      string `json:"stage"`
	ErrorClass string `json:"error_class"`
}

func SessionDiagnostic(action string, identified, policy, isolated bool) *Diagnostic {
	d := &Diagnostic{Stage: action, ErrorClass: "REPLY_UNCONFIRMED"}
	if identified {
		d.Stage = "policy_check"
		d.ErrorClass = "POLICY_UNCONFIRMED"
	}
	if policy {
		d.Stage = "isolation_probe"
		d.ErrorClass = "ISOLATION_UNCONFIRMED"
	}
	if isolated {
		d.Stage = "ready"
		d.ErrorClass = ""
	}
	return d
}

func ValidDiagnostic(d *Diagnostic) bool {
	if d == nil {
		return true
	}
	switch d.Stage {
	case "create", "load", "policy_check", "isolation_probe", "ready":
	default:
		return false
	}
	switch d.ErrorClass {
	case "", "REPLY_UNCONFIRMED", "POLICY_UNCONFIRMED", "ISOLATION_UNCONFIRMED":
		return true
	}
	return false
}

// SessionEventFields preserves the legacy nine-field envelope. The broker still
// validates every outer field; this helper only accounts for the optional object.
func SessionEventFields(raw []byte) int {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return -1
	}
	value, ok := fields["diagnostic"]
	if !ok {
		return 9
	}
	if string(value) == "null" {
		return 10
	}
	d := json.NewDecoder(strings.NewReader(string(value)))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return -1
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || key != "stage" && key != "error_class" {
			return -1
		}
		seen[key] = true
		var text string
		if d.Decode(&text) != nil {
			return -1
		}
	}
	if len(seen) != 2 {
		return -1
	}
	return 10
}
