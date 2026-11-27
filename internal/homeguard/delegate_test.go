package homeguard

import "testing"

// The upgrade installs the server's cgroup delegation with the home
// priority files, under the same backup and rollback.
func TestDesiredFilesDelegateServerCgroup(t *testing.T) {
	got := desiredFiles(nil)[serverDelegate]
	if got != "[Service]\nDelegate=yes\nProtectControlGroups=no\n" || !validPriorityPath(serverDelegate) {
		t.Fatalf("%q", got)
	}
	if validPriorityPath("mcpshka.service.d/99-other.conf") {
		t.Fatal("foreign mcpshka drop-in accepted")
	}
}
