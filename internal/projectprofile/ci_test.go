package projectprofile

import "testing"

func TestDistributedProfiles(t *testing.T) {
	for _, p := range []Profile{diagProfile(), laneProfile()} {
		if e := validateProfile(p); e != nil {
			t.Fatal(e)
		}
		if p.CI == nil || p.CI.Provider != "distributed" || !p.CI.ExternalOnly {
			t.Fatal(p)
		}
		p.CI.Provider = "local"
		if validateProfile(p) == nil {
			t.Fatal("local profile allowed")
		}
	}
}
