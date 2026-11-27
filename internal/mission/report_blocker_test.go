package mission

import (
	"context"
	"errors"
	"testing"
)

func TestReportSeparatesAcceptanceFromUnprovenReleaseStages(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	check := func(accepted string) {
		t.Helper()
		r, err := f.service.Report(context.Background(), s.ID)
		if err != nil || r.Accepted != accepted || r.ImplementationComplete != "UNKNOWN" ||
			r.Integrated != "UNKNOWN" || r.ProductionVerified != "UNKNOWN" {
			t.Fatalf("report overstated missing release proof: %+v %v", r, err)
		}
	}
	check("UNKNOWN")
	f.accepted["P1"] = true
	if _, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	check("UNKNOWN")
	f.accepted["P2"] = true
	if _, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	check("UNKNOWN")
}

func TestReportRemainsReadableWhenDiscoveryIsUnavailable(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	f.service.call = func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, errors.New("discovery unavailable")
	}
	report, err := f.service.Report(context.Background(), s.ID)
	if err != nil || report.State != "BLOCKED" || report.CurrentStep != "P1" || report.NextAction != "capabilities_bootstrap" || len(report.Pending) == 0 {
		t.Fatal(report, err)
	}
}
