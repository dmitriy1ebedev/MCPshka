package providers

import (
	"context"
	"testing"
	"time"
)

type fakeProvider struct{}

func (fakeProvider) Identity(context.Context) (Identity, error) {
	return Identity{ID: "mock", Kind: "reasoner", Trust: "untrusted"}, nil
}
func (fakeProvider) Models(context.Context) ([]string, error) { return []string{"mock"}, nil }
func (fakeProvider) Ask(_ context.Context, r Request) (Observation, error) {
	return Observation{ID: r.ID, PlanFingerprint: r.PlanFingerprint, State: "observed"}, nil
}
func (f fakeProvider) Review(ctx context.Context, r Request) (Observation, error) {
	o, _ := f.Ask(ctx, r)
	return AdvisoryReview(r, o)
}
func (f fakeProvider) Dispatch(ctx context.Context, r Request) (Observation, error) {
	return f.Ask(ctx, r)
}
func (fakeProvider) Reconcile(context.Context, string) (Observation, error) {
	return Observation{State: "UNKNOWN"}, nil
}
func (f fakeProvider) Read(ctx context.Context, r Request) (Observation, error) { return f.Ask(ctx, r) }
func (f fakeProvider) Observe(ctx context.Context, r Request) (Observation, error) {
	return f.Ask(ctx, r)
}

var _ ReasonerProvider = fakeProvider{}
var _ ExecutionProvider = fakeProvider{}
var _ ConnectorProvider = fakeProvider{}
var _ ObserverProvider = fakeProvider{}

func TestProviderContractsQuotaAndAdvisory(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour)
	zero := 0.0
	q := ObservedQuota(&zero, &reset, now, "fake receipt", now)
	if q.State != "EXHAUSTED" || q.ResetAt == nil || q.Remaining == nil || *q.Remaining != 0 {
		t.Fatal(q)
	}
	if q := ObservedQuota(nil, nil, now, "", now); q.State != "UNKNOWN" || q.Remaining != nil {
		t.Fatal(q)
	}
	r := Request{ID: "request", PlanFingerprint: "same-plan"}
	o, err := (fakeProvider{}).Review(context.Background(), r)
	if err != nil || !o.Advisory || o.PlanFingerprint != r.PlanFingerprint {
		t.Fatal(o, err)
	}
	o.PlanFingerprint = "changed"
	if _, err := AdvisoryReview(r, o); err == nil {
		t.Fatal("changed plan")
	}
}
