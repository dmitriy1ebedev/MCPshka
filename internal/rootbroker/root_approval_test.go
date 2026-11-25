package rootbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootApprovalPinsLatestTaskContentAndVersion(t *testing.T) {
	p := rootTaskPermit{ID: strings.Repeat("a", 32), Scope: "homeassistant_automations", StagedSHA: strings.Repeat("b", 64), PlanVersion: rootTaskPlanVersion}
	h := rootTaskApprovalHead{TaskID: p.ID, StagedSHA: p.StagedSHA, PlanVersion: rootTaskPlanVersion}
	if err := validateRootApproval(p, h); err != nil {
		t.Fatal(err)
	}
	cases := map[string]rootTaskApprovalHead{
		"newer_task":          {TaskID: strings.Repeat("c", 32), StagedSHA: p.StagedSHA, PlanVersion: rootTaskPlanVersion},
		"changed_content":     {TaskID: p.ID, StagedSHA: strings.Repeat("d", 64), PlanVersion: rootTaskPlanVersion},
		"unsupported_version": {TaskID: p.ID, StagedSHA: p.StagedSHA, PlanVersion: 99},
		"missing_head":        {},
	}
	for name, head := range cases {
		t.Run(name, func(t *testing.T) {
			if validateRootApproval(p, head) == nil {
				t.Fatal("stale or mismatched approval accepted")
			}
		})
	}
	p.PlanVersion = 0
	if validateRootApproval(p, h) == nil {
		t.Fatal("legacy unversioned mutation admitted")
	}
}

func TestVersionedRootReceiptRequiresAuthorizationProvenance(t *testing.T) {
	now := time.Now().UTC()
	good := rootTaskPermit{ID: strings.Repeat("a", 32), Owner: "owner", Scope: "homeassistant_automations", LiveSHA: strings.Repeat("b", 64), StagedSHA: strings.Repeat("c", 64), State: "authorized", ExpiresAt: now.Add(time.Hour), PlanVersion: rootTaskPlanVersion, AuthMethod: "webauthn", AuthorizedAt: now.Format(time.RFC3339Nano)}
	check := func(p rootTaskPermit) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		path := filepath.Join(t.TempDir(), "permit.json")
		if err = os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = loadPermit(f)
		return err
	}
	if err := check(good); err != nil {
		t.Fatal(err)
	}
	totp := good
	totp.AuthMethod = "totp"
	if err := check(totp); err != nil {
		t.Fatal("TOTP compatibility lost", err)
	}
	for _, method := range []string{"", "unverified"} {
		p := good
		p.AuthMethod = method
		if check(p) == nil {
			t.Fatal("unverified provenance accepted")
		}
	}
	for _, stamp := range []string{"", "not-a-time"} {
		p := good
		p.AuthorizedAt = stamp
		if check(p) == nil {
			t.Fatal("invalid receipt timestamp accepted")
		}
	}
	legacy := good
	legacy.PlanVersion = 0
	legacy.AuthMethod = ""
	legacy.AuthorizedAt = ""
	if err := check(legacy); err != nil {
		t.Fatal("historical receipt lost", err)
	}
}
