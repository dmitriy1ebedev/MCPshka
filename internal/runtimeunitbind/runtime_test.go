package runtimeunitbind

import (
	"strings"
	"testing"
)

func TestReadOnlyScope(t *testing.T) {
	path := "/root/repos/ug2/private/engine-test-0.1.a.23"
	got, mask, err := ReadOnly([]string{path})
	if err != nil || !mask || len(got) != 1 ||
		got[0] != "--property=BindReadOnlyPaths="+path+":"+path ||
		strings.Contains(got[0], "BindPaths=") {
		t.Fatalf("scope %v mask %v err %v", got, mask, err)
	}
	got, mask, err = ReadOnly([]string{"/opt/app/runtime"})
	if err != nil || mask || len(got) != 1 {
		t.Fatalf("non-home scope %v %v %v", got, mask, err)
	}
	for _, bad := range []string{"/", "/root", "/root/repos", "../escape", "/root/repos/x/../y", "/root/repos/x:y", "/root/repos/x\nfoo"} {
		if _, _, err := ReadOnly([]string{bad}); err == nil {
			t.Errorf("accepted unsafe %q", bad)
		}
	}
}
