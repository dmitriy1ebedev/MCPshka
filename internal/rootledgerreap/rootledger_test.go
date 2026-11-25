package rootledgerreap

import "testing"

func TestPinnedRootLedgerReapContract(t *testing.T) {
	for _, op := range []string{"smoke", "apply", "rollback", "status"} {
		if err := validateRootLedgerReap([]string{op, "0123456789abcdef"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"apply", "../root"}, {"kill", "0123456789abcdef"}, {"apply", "0123456789abcdef", "rootledger-app-1"}, {"apply", "AAAAAAAAAAAAAAAA"}} {
		if err := validateRootLedgerReap(args); err == nil {
			t.Fatalf("unsafe capability accepted: %v", args)
		}
	}
}
