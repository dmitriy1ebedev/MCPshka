package mcpserver

import (
	"mcpshka/internal/authn"
	"mcpshka/internal/issues"
	"testing"
)

func TestIssueDiscoveryComplaintCounterUsesMutationIdentity(t *testing.T) {
	store := withIssueStore(t)
	i, err := store.Create(issues.NewIssue{Title: "Missed native tool", Body: "fixture", Priority: "P2"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	c := catalogClient(t, &authn.Identity{ID: "reporter", Role: "operator", Scopes: []string{"issue_update", "issue_get"}})
	args := map[string]any{"id": i.ID, "idempotency_key": "complaint-one", "discovery_defect": map[string]any{"chosen_tools": []string{"external_read"}, "reported_native_alternatives": []string{"fs_read"}, "regression_case": "Read two files with native fs_read"}}
	for n := 0; n < 2; n++ {
		catalogCall(t, c, "issue_update", args)
	}
	got, err := store.Get(i.ID)
	if err != nil || got.DiscoveryComplaints != 1 || len(got.DiscoveryCases) != 1 || got.Category != "catalog-discovery" {
		t.Fatalf("%+v %v", got, err)
	}
	args["idempotency_key"] = "complaint-two"
	catalogCall(t, c, "issue_update", args)
	out := catalogCall(t, c, "issue_get", map[string]any{"id": i.ID})
	card := out
	if card["catalog_discovery_complaints"] != float64(2) {
		t.Fatal(card)
	}
	catalogCall(t, c, "issue_update", map[string]any{"id": i.ID, "append": "ordinary finding", "idempotency_key": "append"})
	got, _ = store.Get(i.ID)
	if got.DiscoveryComplaints != 2 {
		t.Fatal("ordinary finding counted as a complaint")
	}
}
