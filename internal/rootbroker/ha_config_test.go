package rootbroker

import (
	"strings"
	"testing"
)

func TestMaskHASecrets(t *testing.T) {
	in := "api_password: hunter2\ntoken: abc123 # x\nhost: 1.2.3.4\npassword: !secret wifi\n"
	got := maskHASecrets(in)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abc123") || !strings.Contains(got, "!secret wifi") || !strings.Contains(got, "host: 1.2.3.4") {
		t.Fatal(got)
	}
	if _, err := runHAConfig([]string{"read", "secrets.yaml"}); err == nil {
		t.Fatal("secrets.yaml readable")
	}
}
