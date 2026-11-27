package diagprovider

import "testing"

func report(signals string) string {
	return `{"health":{"ok":3,"warn":1,"fail":1,"unknown":0},"signals":[` + signals + `]}`
}

func TestBriefKeepsNonOKFailFirst(t *testing.T) {
	b := Brief(NativeRun{ID: "r", State: "done", Report: report(`{"status":"warn","key":"disk","summary":"low"},{"status":"ok","key":"cpu"},{"status":"fail","key":"unit","summary":"x failed"}`)})
	if b.Fail != 1 || b.Warn != 1 || len(b.Findings) != 2 || b.Findings[0].Key != "unit" {
		t.Fatalf("%+v", b)
	}
}

func TestCompareVerdicts(t *testing.T) {
	disk := `{"status":"warn","key":"disk"}`
	unit := `{"status":"fail","key":"unit"}`
	for name, c := range map[string]struct{ before, after, want string }{
		"fixed":     {disk + "," + unit, `{"status":"ok","key":"disk"}`, "FIXED"},
		"improved":  {disk + "," + unit, disk, "IMPROVED"},
		"unchanged": {disk, disk, "UNCHANGED"},
		"worse":     {disk, disk + "," + unit, "WORSE"},
	} {
		got := Compare(NativeRun{ID: "b", Report: report(c.before)}, NativeRun{ID: "a", Report: report(c.after)})
		if got.Verdict != c.want {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if Compare(NativeRun{}, NativeRun{Report: report(disk)}).Verdict != "INCOMPARABLE" {
		t.Fatal("empty report must be incomparable")
	}
}
