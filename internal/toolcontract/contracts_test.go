package toolcontract

import (
	"testing"
)

func TestContractRejectsMissingSafetyFields(t *testing.T) {
	good := mutationContract{Probe: "state_status", Recovery: "job_cancel", Key: "idempotency_key", OutputLimit: 131072, BeforeAfter: true}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"probe", "recovery", "key", "output", "evidence"} {
		c := good
		switch field {
		case "probe":
			c.Probe = ""
		case "recovery":
			c.Recovery = ""
		case "key":
			c.Key = ""
		case "output":
			c.OutputLimit = 0
		case "evidence":
			c.BeforeAfter = false
		}
		if c.Validate() == nil {
			t.Fatalf("accepted missing %s", field)
		}
	}
}

// BUG-20261027-148: the adopt before probe sent an empty preview request,
// so every project_repo_adopt was held before reaching the broker.
func TestProjectRepoAdoptProbeCarriesSessionAndTarget(t *testing.T) {
	in := map[string]any{"admin_session_id": "s", "path": "/root/repos/x", "source": "/root/x", "plan_sha256": "p"}
	probe, before, err := ProbeArguments("project_repo_adopt", Contracts["project_repo_adopt"].Probe, in, nil, true)
	if err != nil || probe != "project_repo_preview" || before["admin_session_id"] != "s" || before["path"] != "/root/repos/x" || before["source"] != "/root/x" || before["plan_sha256"] != nil {
		t.Fatalf("before probe = %s %v %v", probe, before, err)
	}
	_, after, err := ProbeArguments("project_repo_adopt", "project_repo_preview", in, map[string]any{}, false)
	if err != nil || after["path"] != "/root/repos/x" || after["source"] != nil {
		t.Fatalf("after probe = %v %v", after, err)
	}
}

func TestAllMutationContractsValidate(t *testing.T) {
	for name, c := range Contracts {
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, ok := Contracts["new_unreviewed_tool"]; ok {
		t.Fatal("unexpected contract")
	}
}

func TestLeaseRecoveryContractsReplayOnlyJournaledMutations(t *testing.T) {
	for _, name := range []string{"project_lease_resume", "project_lease_admin_recover", "project_lease_admin_stale_release"} {
		c, ok := Contracts[name]
		if !ok || !c.ReplayPending {
			t.Fatal("journaled recovery cannot reconcile lost reply", name)
		}
	}
}
