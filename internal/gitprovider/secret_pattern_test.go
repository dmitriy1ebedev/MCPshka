package gitprovider

import "testing"

func TestSecretPatternNeedsKeyBody(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"+        \"-----BEGIN OPENSSH PRIVATE KEY-----\\n\"\n+        + \"A\" * 80\n", false},
		{"+-----BEGIN OPENSSH PRIVATE KEY-----\n+b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n", true},
		{"key: \"-----BEGIN RSA PRIVATE KEY-----\\nMIIEowIBAAKCAQEAuWZyIkJxHHh9sWOJ0D6DyFrSV2mYdpS6aBHxgfW5o9\"", true},
		{"-----BEGIN PRIVATE KEY-----\r\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\r\n", true},
		{"token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ12", true},
		{"docs mention -----BEGIN OPENSSH PRIVATE KEY----- headers", false},
	} {
		if got := secretRE.MatchString(c.in); got != c.want {
			t.Errorf("%q: %v, want %v", c.in, got, c.want)
		}
	}
}
