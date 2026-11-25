package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/statestore"
)

func TestRecipeAbandonTombstonesOrMatchesOneUseClaim(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	manifest := filepath.Join(t.TempDir(), "permit.manifest.json")
	fields := map[string]string{"permit_id": "p", "repo": "/root/repos/diag", "commit": "c", "package": recipeTestPackage, "timeout_seconds": "1800"}
	const unclaimed, claimed, foreign = "11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"

	// Never claimed: the tombstone makes any later constructor fail its claim.
	if err := abandonRecipeClaim(c, unclaimed, fields, manifest); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.PutIfAbsent("executor/bootstrap/"+unclaimed+".json", []byte(`{}`)); err != nil || ok {
		t.Fatal("tombstone did not consume the one-use claim", ok, err)
	}
	// Claimed by the failed constructor with the same identity.
	raw, _ := json.Marshal(fields)
	if _, err := db.PutIfAbsent("executor/bootstrap/"+claimed+".json", raw); err != nil {
		t.Fatal(err)
	}
	if err := abandonRecipeClaim(c, claimed, fields, manifest); err != nil {
		t.Fatal(err)
	}
	// A claim for another source is never settled through this binding.
	other := map[string]string{}
	for k, v := range fields {
		other[k] = v
	}
	other["commit"] = "d"
	raw, _ = json.Marshal(other)
	if _, err := db.PutIfAbsent("executor/bootstrap/"+foreign+".json", raw); err != nil {
		t.Fatal(err)
	}
	if abandonRecipeClaim(c, foreign, fields, manifest) == nil {
		t.Fatal("foreign claim abandoned")
	}
	// A sealed manifest means a permit exists, even with a matching claim.
	if err := os.WriteFile(manifest, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if abandonRecipeClaim(c, claimed, fields, manifest) == nil {
		t.Fatal("sealed permit abandoned")
	}
}
