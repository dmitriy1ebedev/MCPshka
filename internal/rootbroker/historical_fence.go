package rootbroker

// Read-only root attestation, with an exact-release bootstrap bridge for fences
// not yet migrated to permanent retirement records.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	legacyFenceID    = "1792159092900-1e327387d562"
	legacyUpgradeID  = "b738d46dd7322e50e3768769fcd360a9"
	legacyWebAuthnID = "88eb8e95c2e2f4cc769314ae6f409e65"
	legacyReceiptSHA = "aa85f2c751a5d6283a180422f0027ff0de4d131172f91ce1901b75e6a0008abd"
	legacyOldGate    = "43be6c9e5c74b54f8a1eb4481d3e2cfb1a2b28780580260be0c5796219d266b1"
	legacyOldGuarded = "6b5a3addc1157b5a7e81e9f306c6a19e2e85d65407e990c05d1a1ea557b6b842"
	legacyNewGate    = "56369bbbd59b7a55b216b85494f7d1befd29d3be61241dbb819d29b11a908327"
	legacyNewGuarded = "7894e11a1773e02ca0b695babf882d6727dded166e27d37823485e80fe44e483"
)

var auditMarkerRE = regexp.MustCompile(`^[0-9]{19}-[A-Z_]+\.json$`)

type historicalPins struct {
	ID, Session, Receipt, OldGate, OldGuarded, NewGate, NewGuarded string
}

var approvedHistoricalPins = historicalPins{
	legacyFenceID, legacyWebAuthnID, legacyReceiptSHA, legacyOldGate,
	legacyOldGuarded, legacyNewGate, legacyNewGuarded,
}

type historicalFence struct {
	AuthorizationSessionID string `json:"authorization_session_id"`
	DeploymentID           string `json:"deployment_id"`
	FencedAt               int64  `json:"fenced_at"`
	GateSHA256             string `json:"gate_sha256"`
	HelperSHA256           string `json:"helper_sha256"`
	ObservedCurrentSHA256  string `json:"observed_current_sha256"`
	Reason                 string `json:"reason"`
	ReceiptSHA256          string `json:"receipt_sha256"`
	Schema                 string `json:"schema"`
}
type historicalJournalEvent struct {
	Schema string `json:"schema"`
	State  string `json:"state"`
	AtNS   string `json:"at_ns"`
}
type historicalFenceProof struct {
	Schema           string `json:"schema"`
	State            string `json:"state"`
	DeploymentID     string `json:"deployment_id"`
	ReceiptSHA256    string `json:"receipt_sha256"`
	FenceSHA256      string `json:"fence_sha256"`
	UpgradeID        string `json:"upgrade_id,omitempty"`
	RetirementSHA256 string `json:"retirement_sha256,omitempty"`
}

func historicSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func decodeUniqueHistorical(raw []byte, out any, count int) error {
	if len(raw) == 0 || len(raw) > 8192 {
		return errors.New("historical record too large: HOLD")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return errors.New("invalid historical object: HOLD")
	}
	seen := map[string]bool{}
	for d.More() {
		tok, e = d.Token()
		k, ok := tok.(string)
		if e != nil || !ok || seen[k] {
			return errors.New("duplicate historical field: HOLD")
		}
		seen[k] = true
		var x json.RawMessage
		if e = d.Decode(&x); e != nil {
			return e
		}
	}
	if _, e = d.Token(); e != nil {
		return e
	}
	var x any
	if d.Decode(&x) != io.EOF || len(seen) != count {
		return errors.New("historical object extra/missing fields: HOLD")
	}
	d = json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

// Pure policy verifier: the broker adds the root-only file provenance check.
func verifyHistoricalBridge(fenceRaw, oldGate, oldGuarded, newGate, newGuarded, eventRaw []byte, marker string, now time.Time) error {
	return verifyHistoricalBridgePins(approvedHistoricalPins, fenceRaw, oldGate, oldGuarded, newGate, newGuarded, eventRaw, marker, now)
}
func verifyHistoricalBridgePins(p historicalPins, fenceRaw, oldGate, oldGuarded, newGate, newGuarded, eventRaw []byte, marker string, now time.Time) error {
	var f historicalFence
	if e := decodeUniqueHistorical(fenceRaw, &f, 9); e != nil {
		return e
	}
	if f.Schema != "mcpshka.deployment-fence/v1" || f.DeploymentID != p.ID ||
		f.AuthorizationSessionID != p.Session || f.ReceiptSHA256 != p.Receipt ||
		f.GateSHA256 != p.OldGate || f.HelperSHA256 != p.OldGuarded ||
		f.FencedAt <= 0 || f.FencedAt > now.Unix() || strings.TrimSpace(f.Reason) == "" ||
		len(f.Reason) > 200 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(f.ObservedCurrentSHA256) {
		return errors.New("historical fence policy mismatch: HOLD")
	}
	if historicSHA(oldGate) != p.OldGate || historicSHA(oldGuarded) != p.OldGuarded ||
		historicSHA(newGate) != p.NewGate || historicSHA(newGuarded) != p.NewGuarded {
		return errors.New("historical gate/guarded lineage mismatch: HOLD")
	}
	if !auditMarkerRE.MatchString(marker) || !strings.HasSuffix(marker, "-SUCCEEDED.json") {
		return errors.New("historical journal not terminal success: HOLD")
	}
	var event historicalJournalEvent
	if e := decodeUniqueHistorical(eventRaw, &event, 3); e != nil {
		return e
	}
	stamp := strings.TrimSuffix(marker, "-SUCCEEDED.json")
	nano, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || event.Schema != "mcpshka.paired-upgrade-audit/v1" ||
		event.State != "SUCCEEDED" || event.AtNS != stamp ||
		nano <= f.FencedAt*1e9 || nano > now.UnixNano() {
		return errors.New("historical journal completion mismatch: HOLD")
	}
	return nil
}

func historicalRootDir(path string, mode os.FileMode) error {
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != 0 || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != mode {
		return errors.New("unsafe historical root directory: HOLD")
	}
	return nil
}
func historicalRootRead(path string, limit int64) ([]byte, error) {
	return historicalRootReadOwned(path, limit, 0)
}

func historicalRootReadOwned(path string, limit int64, owner uint32) ([]byte, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	a, e := f.Stat()
	if e != nil {
		return nil, e
	}
	st, ok := a.Sys().(*syscall.Stat_t)
	if !ok || !a.Mode().IsRegular() || st.Uid != owner || st.Nlink != 1 ||
		a.Mode().Perm()&0022 != 0 || a.Size() <= 0 || a.Size() > limit {
		return nil, errors.New("unsafe historical root file: HOLD")
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	b, e := f.Stat()
	if e != nil {
		return nil, e
	}
	named, e := os.Lstat(path)
	if e != nil || int64(len(raw)) != a.Size() || !os.SameFile(a, b) ||
		!os.SameFile(b, named) || a.ModTime() != b.ModTime() || a.Mode() != b.Mode() {
		return nil, errors.New("historical root file changed: HOLD")
	}
	return raw, nil
}

// Permanent records need no installer images. The pre-migration bridge admits
// only the exact verified 0.1.134 transition; outcomes remain UNKNOWN.
func runHistoricalFenceProof(ctx context.Context, args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("root broker required: HOLD")
	}
	if len(args) != 3 || !adminDeploymentID.MatchString(args[0]) || !validHash(args[1]) || !validHash(args[2]) {
		return nil, errors.New("unknown historical operation: HOLD")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	const fenceDir = "/var/lib/mcpshka-deploy-fences"
	if e := historicalRootDir(fenceDir, 0755); e != nil {
		return nil, e
	}
	if proof, e := readPermanentRetirement(fenceDir, args, time.Now(), historicalRootRead); !errors.Is(e, os.ErrNotExist) {
		return proof, e
	}
	if args[0] != legacyFenceID || args[1] != legacyReceiptSHA {
		return nil, errors.New("unsealed historical operation: HOLD")
	}
	const ledger = "/var/lib/mcpshka-executor"
	audit := filepath.Join(ledger, "coordinated-upgrades")
	work := filepath.Join(audit, legacyUpgradeID)
	for _, dir := range []struct {
		p string
		m os.FileMode
	}{
		{fenceDir, 0755}, {ledger, 0700}, {audit, 0700}, {work, 0700},
	} {
		if e := historicalRootDir(dir.p, dir.m); e != nil {
			return nil, e
		}
	}
	read := historicalRootRead
	fence, e := read(filepath.Join(fenceDir, legacyFenceID+".json"), 8192)
	if e != nil {
		return nil, e
	}
	if historicSHA(fence) != args[2] {
		return nil, errors.New("changed historical fence: HOLD")
	}
	oldGate, e := read(filepath.Join(work, "gate.previous"), 131072)
	if e != nil {
		return nil, e
	}
	oldGuarded, e := read(filepath.Join(work, "guarded.previous"), 131072)
	if e != nil {
		return nil, e
	}
	newGate, e := read("/usr/local/libexec/mcpshka-self-deploy", 131072)
	if e != nil {
		return nil, e
	}
	newGuarded, e := read("/usr/local/libexec/mcpshka-self-deploy-guarded", 131072)
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(work)
	if e != nil {
		return nil, e
	}
	if len(entries) > 128 {
		return nil, errors.New("historical journal budget: HOLD")
	}
	names := []string{}
	for _, v := range entries {
		if strings.HasSuffix(v.Name(), ".json") {
			if v.IsDir() || !auditMarkerRE.MatchString(v.Name()) {
				return nil, errors.New("unexpected root audit phase: HOLD")
			}
			names = append(names, v.Name())
		}
	}
	if len(names) == 0 {
		return nil, errors.New("missing historical journal: HOLD")
	}
	sort.Strings(names)
	last := names[len(names)-1]
	event, e := read(filepath.Join(work, last), 2048)
	if e != nil {
		return nil, e
	}
	if e = verifyHistoricalBridge(fence, oldGate, oldGuarded, newGate, newGuarded, event, last, time.Now().UTC()); e != nil {
		return nil, e
	}
	return json.Marshal(historicalFenceProof{
		Schema: "mcpshka.historical-fence-proof/v1", State: "VERIFIED_NONREPLAYABLE",
		DeploymentID: legacyFenceID, ReceiptSHA256: legacyReceiptSHA,
		FenceSHA256: historicSHA(fence), UpgradeID: legacyUpgradeID,
	})
}
