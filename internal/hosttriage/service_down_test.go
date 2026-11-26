package hosttriage

import "testing"

func TestMaskedOrDisabledServiceIsNotDown(t *testing.T) {
	for _, c := range []struct {
		active, load, file string
		down               bool
	}{
		{"inactive", "masked", "masked", false},
		{"failed", "loaded", "masked", false},
		{"inactive", "loaded", "disabled", false},
		{"active", "loaded", "enabled", false},
		{"inactive", "not-found", "", false},
		{"failed", "loaded", "enabled", true},
		{"inactive", "loaded", "enabled", true},
	} {
		if got := serviceDown(c.active, c.load, c.file); got != c.down {
			t.Fatalf("serviceDown(%q,%q,%q)=%v want %v", c.active, c.load, c.file, got, c.down)
		}
	}
}
