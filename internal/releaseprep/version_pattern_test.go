package releaseprep

import "testing"

func TestVersionPatternAcceptsTheZeroTwoLine(t *testing.T) {
	for _, v := range []string{"0.1.196", "0.1.197", "0.2.1", "0.10.0"} {
		if !versionPattern.MatchString(v) {
			t.Fatalf("%s rejected", v)
		}
	}
	for _, v := range []string{"1.0.0", "0.2", "0.2.1-rc1", "0.2.1;rm", "v0.2.1"} {
		if versionPattern.MatchString(v) {
			t.Fatalf("%s accepted", v)
		}
	}
}
