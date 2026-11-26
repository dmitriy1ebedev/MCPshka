package rootbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

func testAdminSession(now time.Time) AdminSession {
	return AdminSession{Schema: AdminSessionSchema, ID: strings.Repeat("a", 32), Owner: "owner:test", Policy: AdminSessionPolicy, AuthMethod: "webauthn", AuthorizedAt: now.UTC().Format(time.RFC3339Nano), Generation: 1, State: "active"}
}

func adminTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAdminSessionSurvivesRestartWithinItsTaskWindow(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	p := testAdminSession(now)
	if err := saveAdminSession(root, p, now); err != nil {
		t.Fatal(err)
	}
	// A fresh read models another broker process after a restart or lost tunnel.
	later := now.Add(20 * time.Hour)
	got, err := readAdminSession(root, later)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireAdminSession(got, p.ID, p.Owner, p.Generation, later); err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("grant changed after restart: %+v", got)
	}
	raw, err := os.ReadFile(filepath.Join(root, adminSessionFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "expires") {
		t.Fatal("expiry must be derived, not written into the grant record")
	}
}

func TestAdminSessionExpiresAfterItsCapAndAllowsANewFaceID(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	p := testAdminSession(now)
	if err := saveAdminSession(root, p, now); err != nil {
		t.Fatal(err)
	}
	late := now.Add(AdminSessionMaxAge + time.Hour)
	got, err := readAdminSession(root, late)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "expired" {
		t.Fatalf("forgotten grant still active after cap: %+v", got)
	}
	if err := requireAdminSession(got, p.ID, p.Owner, p.Generation, late); err == nil {
		t.Fatal("expired grant authorized work")
	}
	if ended, err := endAdminSession(root, p.ID, p.Owner, "closed", late); err != nil || ended.State != "expired" {
		t.Fatal("closing an expired grant must be a no-op", ended, err)
	}
	next := testAdminSession(late)
	next.ID, next.Generation = strings.Repeat("b", 32), p.Generation+1
	if err := saveAdminSession(root, next, late); err != nil {
		t.Fatal("new Face ID grant could not follow an expired one", err)
	}
	if got, err := readAdminSession(root, late); err != nil || got.State != "active" {
		t.Fatal("fresh grant not active", got, err)
	}
}

func TestAdminSessionRevocationIsDurableAndIdempotent(t *testing.T) {
	for _, state := range []string{"revoked", "closed"} {
		t.Run(state, func(t *testing.T) {
			root, now := adminTestRoot(t), time.Now().UTC()
			p := testAdminSession(now)
			if err := saveAdminSession(root, p, now); err != nil {
				t.Fatal(err)
			}
			if _, err := endAdminSession(root, p.ID, "other-owner", state, now); err == nil {
				t.Fatal("foreign owner ended grant")
			}
			ended, err := endAdminSession(root, p.ID, p.Owner, state, now)
			if err != nil {
				t.Fatal(err)
			}
			if ended.State != state || ended.Generation != p.Generation+1 {
				t.Fatalf("invalid revocation: %+v", ended)
			}
			again, err := endAdminSession(root, p.ID, p.Owner, state, now)
			if err != nil || again != ended {
				t.Fatalf("repeat changed revocation: %+v %v", again, err)
			}
			fresh, err := readAdminSession(root, now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if err := requireAdminSession(fresh, p.ID, p.Owner, p.Generation, now.Add(time.Hour)); err == nil {
				t.Fatal("revoked grant admitted effect")
			}
		})
	}
}

func TestAdminSessionOwnerAndGenerationFence(t *testing.T) {
	now := time.Now().UTC()
	p := testAdminSession(now)
	for _, tc := range []struct {
		id, owner  string
		generation int64
	}{
		{p.ID, "another", 1}, {p.ID, "local-stdio", 1}, {p.ID, p.Owner, 2}, {strings.Repeat("b", 32), p.Owner, 1},
	} {
		if requireAdminSession(p, tc.id, tc.owner, tc.generation, now) == nil {
			t.Fatalf("accepted foreign binding: %+v", tc)
		}
	}
}

func TestAdminPendingChallengeCanBeCancelledBeforeFaceID(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	p := passkeySession{PlanVersion: adminSessionPlanVersion, ID: strings.Repeat("a", 32), Owner: "owner:test", Scope: adminSessionScope, Policy: AdminSessionPolicy, ExpiresAt: now.Add(time.Minute)}
	p.Data.UserVerification = protocol.VerificationRequired
	path := filepath.Join(root, "passkey-"+p.ID+".json")
	if err := writeRootOnly(path, p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := endAdminSession(root, p.ID, "other-owner", "revoked", now); err == nil {
		t.Fatal("foreign challenge cancelled")
	}
	for range 2 {
		out, err := endAdminSession(root, p.ID, p.Owner, "revoked", now)
		if err != nil || out.State != "revoked" {
			t.Fatalf("pending cancellation: %+v %v", out, err)
		}
	}
	fresh, err := readPasskeySession(path)
	if err != nil {
		t.Fatal(err)
	}
	if validateAdminChallenge(fresh, AdminSession{State: "not_found"}, now) == nil {
		t.Fatal("cancelled challenge admitted after restart")
	}
	current, err := readAdminSession(root, now)
	if err != nil || current.State != "not_found" {
		t.Fatalf("cancel created authority: %+v %v", current, err)
	}
}

func TestAdminSessionCorruptAndUnsafeStateHolds(t *testing.T) {
	now := time.Now().UTC()
	raw, err := json.Marshal(testAdminSession(now))
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		string(raw[:len(raw)-1]) + `,"state":"active"}`,
		string(raw[:len(raw)-1]) + `,"expires_at":"2099-01-01T00:00:00Z"}`,
		strings.Replace(string(raw), `"owner"`, `"Owner"`, 1),
		string(raw) + `{}`, `{}`, `null`, strings.Replace(string(raw), `"generation":1`, `"generation":0`, 1),
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, adminSessionFile), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readAdminSession(root, now); err == nil {
			t.Fatalf("corrupt record admitted: %s", content)
		}
	}
	for _, kind := range []string{"world-readable", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, adminSessionFile)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "world-readable":
				err = os.Chmod(path, 0644)
			case "symlink":
				target := filepath.Join(root, "target")
				if err = os.Rename(path, target); err == nil {
					err = os.Symlink(target, path)
				}
			case "hardlink":
				err = os.Link(path, filepath.Join(root, "alias"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readAdminSession(root, now); err == nil {
				t.Fatal("unsafe record admitted")
			}
		})
	}
}

func TestAdminSessionLockConflictPreservesGrant(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	p := testAdminSession(now)
	if err := saveAdminSession(root, p, now); err != nil {
		t.Fatal(err)
	}
	lock, err := openAdminSessionLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := endAdminSession(root, p.ID, p.Owner, "revoked", now); err == nil {
		t.Fatal("second writer bypassed session lock")
	}
	got, err := readAdminSession(root, now)
	if err != nil || got != p {
		t.Fatalf("lock conflict changed grant: %+v %v", got, err)
	}
}

func TestAdminSessionUnsafeAuditCannotPublishGrant(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	target := filepath.Join(root, "audit-target")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "admin-session-audit.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := saveAdminSession(root, testAdminSession(now), now); err == nil {
		t.Fatal("unsafe audit admitted grant")
	}
	current, err := readAdminSession(root, now)
	if err != nil || current.State != "not_found" {
		t.Fatalf("unaudited authority published: %+v %v", current, err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "preserve" {
		t.Fatal("audit target modified")
	}
}

func TestAdminChallengeCannotPromoteHAOrReplayAfterRevocation(t *testing.T) {
	now := time.Now().UTC()
	current := AdminSession{State: "not_found"}
	p := passkeySession{PlanVersion: adminSessionPlanVersion, ID: strings.Repeat("a", 32), Owner: "owner:test", Scope: adminSessionScope, Policy: AdminSessionPolicy, ExpiresAt: now.Add(time.Minute)}
	p.Data.UserVerification = protocol.VerificationRequired
	if err := validateAdminChallenge(p, current, now); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*passkeySession){
		func(p *passkeySession) { p.Scope = "homeassistant_automations" },
		func(p *passkeySession) { p.PlanVersion = 1 },
		func(p *passkeySession) { p.Policy = "" },
		func(p *passkeySession) { p.Owner = "local-stdio" },
		func(p *passkeySession) { p.Data.UserVerification = protocol.VerificationPreferred },
	} {
		bad := p
		mutation(&bad)
		if validateAdminChallenge(bad, current, now) == nil {
			t.Fatal("unbound challenge admitted")
		}
	}
	if validateAdminChallenge(p, current, now.Add(2*time.Minute)) == nil {
		t.Fatal("expired challenge admitted")
	}
	current.Generation = 2
	current.State = "revoked"
	if validateAdminChallenge(p, current, now) == nil {
		t.Fatal("old challenge revived revoked authority")
	}
}

func TestAdminPermitDoesNotExpireButLegacyPermitStillDoes(t *testing.T) {
	now := time.Now().UTC()
	p := rootTaskPermit{PlanVersion: rootTaskPlanVersion, Owner: "owner:test", State: "authorized", AuthMethod: "webauthn", AdminSessionID: strings.Repeat("a", 32), AdminGeneration: 1}
	if err := validateTaskAccess(p, p.Owner, now.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	p.AdminSessionID = ""
	p.AdminGeneration = 0
	p.ExpiresAt = now.Add(time.Minute)
	if err := validateTaskAccess(p, p.Owner, now.Add(2*time.Minute)); err == nil {
		t.Fatal("legacy TTL bypassed")
	}
}

func TestHistoricalFenceBridgeRequiresExactRootChain(t *testing.T) {
	now := time.Unix(1792256500, 0).UTC()
	oldGate, oldGuarded := []byte("root old gate"), []byte("root old guarded")
	newGate, newGuarded := []byte("root new gate"), []byte("root new guarded")
	p := historicalPins{
		ID: legacyFenceID, Session: legacyWebAuthnID, Receipt: historicSHA([]byte("old receipt")),
		OldGate: historicSHA(oldGate), OldGuarded: historicSHA(oldGuarded),
		NewGate: historicSHA(newGate), NewGuarded: historicSHA(newGuarded),
	}
	fence := historicalFence{
		Schema: "mcpshka.deployment-fence/v1", DeploymentID: p.ID,
		AuthorizationSessionID: p.Session, ReceiptSHA256: p.Receipt,
		GateSHA256: p.OldGate, HelperSHA256: p.OldGuarded,
		ObservedCurrentSHA256: strings.Repeat("d", 64),
		FencedAt:              now.Add(-15 * time.Minute).Unix(), Reason: "root approved historical nonreplay",
	}
	stamp := strconv.FormatInt(now.Add(-time.Minute).UnixNano(), 10)
	marker := stamp + "-SUCCEEDED.json"
	event, _ := json.Marshal(historicalJournalEvent{Schema: "mcpshka.paired-upgrade-audit/v1", State: "SUCCEEDED", AtNS: stamp})
	sealed, _ := json.Marshal(fence)
	check := func(f, oldG, oldH, newG, newH, evt []byte, name string) error {
		return verifyHistoricalBridgePins(p, f, oldG, oldH, newG, newH, evt, name, now)
	}
	if e := check(sealed, oldGate, oldGuarded, newGate, newGuarded, event, marker); e != nil {
		t.Fatalf("valid historical root chain rejected: %v", e)
	}
	for _, tc := range []struct {
		name                           string
		f, oldG, oldH, newG, newH, evt []byte
		marker                         string
	}{
		{"changed-old-gate", sealed, []byte("foreign"), oldGuarded, newGate, newGuarded, event, marker},
		{"changed-old-guarded", sealed, oldGate, []byte("foreign"), newGate, newGuarded, event, marker},
		{"changed-new-gate", sealed, oldGate, oldGuarded, []byte("foreign"), newGuarded, event, marker},
		{"changed-new-guarded", sealed, oldGate, oldGuarded, newGate, []byte("foreign"), event, marker},
		{"fake-phase", sealed, oldGate, oldGuarded, newGate, newGuarded, event, stamp + "-ROLLED_BACK.json"},
		{"trailing-json", append(append([]byte{}, sealed...), []byte("{}")...), oldGate, oldGuarded, newGate, newGuarded, event, marker},
		{"duplicate-key", []byte(strings.Replace(string(sealed), `"schema":`, `"schema":"x","schema":`, 1)), oldGate, oldGuarded, newGate, newGuarded, event, marker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if e := check(tc.f, tc.oldG, tc.oldH, tc.newG, tc.newH, tc.evt, tc.marker); e == nil {
				t.Fatal("untrusted root evidence accepted")
			}
		})
	}
	wrong := fence
	wrong.ReceiptSHA256 = strings.Repeat("a", 64)
	raw, _ := json.Marshal(wrong)
	if e := check(raw, oldGate, oldGuarded, newGate, newGuarded, event, marker); e == nil {
		t.Fatal("foreign receipt accepted")
	}
	wrong = fence
	wrong.AuthorizationSessionID = strings.Repeat("e", 32)
	raw, _ = json.Marshal(wrong)
	if e := check(raw, oldGate, oldGuarded, newGate, newGuarded, event, marker); e == nil {
		t.Fatal("foreign passkey owner accepted")
	}
}
