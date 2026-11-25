package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/brokerclient"
)

const deploymentFences = "/var/lib/mcpshka-deploy-fences"

var (
	fenceDeploymentID = regexp.MustCompile(`^[0-9]{13}-[a-f0-9]{12}$`)
	fenceSessionID    = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type deploymentFence struct {
	AuthorizationSessionID string `json:"authorization_session_id"`
	Schema                 string `json:"schema"`
	DeploymentID           string `json:"deployment_id"`
	ReceiptSHA256          string `json:"receipt_sha256"`
	FencedAt               int64  `json:"fenced_at"`
	Reason                 string `json:"reason"`
	GateSHA256             string `json:"gate_sha256"`
	HelperSHA256           string `json:"helper_sha256"`
	ObservedCurrentSHA256  string `json:"observed_current_sha256"`
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func rootEvidence(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	pin, err := candidateMetadata(path, before)
	if err != nil || !before.Mode().IsRegular() || pin.UID != 0 || pin.Links != 1 || before.Mode().Perm()&0022 != 0 || before.Size() <= 0 || before.Size() > limit {
		return nil, errors.New("unsafe deployment fence evidence: HOLD")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	final, err := candidateMetadata(path, after)
	named, statErr := os.Lstat(path)
	if err != nil || final != pin || int64(len(raw)) != before.Size() || statErr != nil || !os.SameFile(after, named) {
		return nil, errors.New("deployment fence evidence changed: HOLD")
	}
	return raw, nil
}

func validateFence(raw []byte, id, receiptHash, gateHash, helperHash string, now time.Time) error {
	if len(raw) > 8192 {
		return errors.New("deployment fence budget: HOLD")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid deployment fence: HOLD")
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return errors.New("duplicate deployment fence field: HOLD")
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("trailing deployment fence data: HOLD")
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f deploymentFence
	if err := dec.Decode(&f); err != nil {
		return err
	}
	if len(seen) != 9 || f.Schema != "mcpshka.deployment-fence/v1" || f.DeploymentID != id || !fenceDeploymentID.MatchString(id) || f.ReceiptSHA256 != receiptHash || f.GateSHA256 != gateHash || f.HelperSHA256 != helperHash || !fenceSessionID.MatchString(f.AuthorizationSessionID) || f.FencedAt <= 0 || f.FencedAt > now.Unix() || strings.TrimSpace(f.Reason) == "" || len(f.Reason) > 200 {
		return errors.New("deployment fence pin mismatch: HOLD")
	}
	for _, hash := range []string{f.ReceiptSHA256, f.GateSHA256, f.HelperSHA256, f.ObservedCurrentSHA256} {
		if len(hash) != 64 {
			return errors.New("invalid deployment fence digest: HOLD")
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || hex.EncodeToString(decoded) != hash {
			return errors.New("invalid deployment fence digest: HOLD")
		}
	}
	for _, c := range f.Reason {
		if c < 32 {
			return errors.New("invalid deployment fence reason: HOLD")
		}
	}
	return nil
}

func observeStoredStatus(s Status, raw []byte, now time.Time) (Status, error) {
	s, err := observeStatus(s, now)
	if err != nil || terminal(s.State) {
		return s, err
	}
	info, err := os.Lstat(deploymentFences)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	real, err := filepath.EvalSymlinks(deploymentFences)
	if err != nil || real != deploymentFences || !info.IsDir() || !ok || st.Uid != 0 || info.Mode().Perm() != 0755 {
		return s, errors.New("unsafe deployment fence directory: HOLD")
	}
	fence, err := rootEvidence(filepath.Join(deploymentFences, s.DeploymentID+".json"), 8192)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	// An existing retirement must verify; corrupt evidence cannot fall back to
	// a matching old installer after rollback.
	if _, err := os.Lstat(filepath.Join(deploymentFences, s.DeploymentID+".retired.json")); err == nil {
		proof, err := rootHistoricalProof(s, raw, fence)
		return permanentFenceStatus(s, fence, proof, err)
	} else if !os.IsNotExist(err) {
		return s, err
	}
	gate, err := rootEvidence(InstalledSelfDeploy, 131072)
	if err != nil {
		return s, err
	}
	helper, err := rootEvidence("/usr/local/libexec/mcpshka-self-deploy-guarded", 131072)
	if err != nil {
		return s, err
	}
	return withVerifiedFence(s, raw, fence, gate, helper, now)
}

func permanentFenceStatus(s Status, fence []byte, schema string, proofErr error) (Status, error) {
	if proofErr != nil {
		return s, proofErr
	}
	if schema != "mcpshka.historical-fence-proof/v2" {
		return s, errors.New("permanent retirement proof unavailable: HOLD")
	}
	return fencedStatus(s, fence, true), nil
}

var errHistoricalProofUnavailable = errors.New("historical proof source unavailable")

// The unprivileged daemon asks the root broker for historical attestation.
// Root broker workers have no broker socket and answer in-process instead.
var historicalProofSource = func(ctx context.Context, id, raw, fence string) ([]byte, error) {
	if !brokerclient.Enabled() {
		return nil, errHistoricalProofUnavailable
	}
	return brokerclient.Run(ctx, "historical-fence", id, raw, fence)
}

// UseRootHistoricalProof binds attestation to the root broker's own verifier.
// Only root broker worker entrypoints may call it, before any deploy reads.
func UseRootHistoricalProof(prove func(context.Context, []string) ([]byte, error)) {
	historicalProofSource = func(ctx context.Context, id, raw, fence string) ([]byte, error) {
		return prove(ctx, []string{id, raw, fence})
	}
}

// The exact 0.1.134 root upgrade preserved a previous gate and helper in a
// protected success journal. Ask the privileged broker to authenticate those
// historical root images instead of treating a legitimate upgrade as a revoked
// fence. No assertion from the unprivileged daemon or the receipt can do this.
func rootHistoricalProof(s Status, raw, fence []byte) (string, error) {
	if !fenceDeploymentID.MatchString(s.DeploymentID) {
		return "", errors.New("historical root attestation unavailable: HOLD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proof, err := historicalProofSource(ctx, s.DeploymentID, digest(raw), digest(fence))
	if errors.Is(err, errHistoricalProofUnavailable) {
		return "", errors.New("historical root attestation unavailable: HOLD")
	}
	if err != nil {
		return "", fmt.Errorf("historical root attestation failed: HOLD: %w", err)
	}
	if err := verifyHistoricalProof(proof, s.DeploymentID, digest(raw), digest(fence)); err != nil {
		return "", err
	}
	var result struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(proof, &result); err != nil {
		return "", err
	}
	return result.Schema, nil
}

func verifyHistoricalProof(raw []byte, id, receiptHash, fenceHash string) error {
	if len(raw) == 0 || len(raw) > 2048 {
		return errors.New("historical attestation size: HOLD")
	}
	var result struct {
		Schema           string `json:"schema"`
		State            string `json:"state"`
		DeploymentID     string `json:"deployment_id"`
		ReceiptSHA256    string `json:"receipt_sha256"`
		FenceSHA256      string `json:"fence_sha256"`
		UpgradeID        string `json:"upgrade_id"`
		RetirementSHA256 string `json:"retirement_sha256"`
	}
	// Count tokens before decoding: encoding/json otherwise accepts duplicate keys.
	keys := json.NewDecoder(bytes.NewReader(raw))
	first, err := keys.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("invalid root proof object: HOLD")
	}
	seen := map[string]bool{}
	for keys.More() {
		key, err := keys.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return errors.New("duplicate root proof field: HOLD")
		}
		seen[name] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := keys.Token(); err != nil {
		return err
	}
	for _, name := range []string{"schema", "state", "deployment_id", "receipt_sha256", "fence_sha256"} {
		if !seen[name] {
			return errors.New("root proof field missing: HOLD")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return fmt.Errorf("invalid historical root attestation: %w", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF ||
		result.State != "VERIFIED_NONREPLAYABLE" ||
		result.DeploymentID != id || !fenceDeploymentID.MatchString(id) ||
		result.ReceiptSHA256 != receiptHash ||
		result.FenceSHA256 != fenceHash || len(seen) != 6 {
		return errors.New("historical root attestation identity mismatch: HOLD")
	}
	switch result.Schema {
	case "mcpshka.historical-fence-proof/v1":
		if !seen["upgrade_id"] || id != "1792159092900-1e327387d562" || result.UpgradeID != "b738d46dd7322e50e3768769fcd360a9" {
			return errors.New("legacy root upgrade mismatch: HOLD")
		}
	case "mcpshka.historical-fence-proof/v2":
		if !seen["retirement_sha256"] || len(result.RetirementSHA256) != 64 || strings.Trim(result.RetirementSHA256, "0123456789abcdef") != "" {
			return errors.New("permanent root proof mismatch: HOLD")
		}
	default:
		return errors.New("unknown root proof schema: HOLD")
	}
	return nil
}

func withVerifiedFence(s Status, raw, fence, gate, helper []byte, now time.Time) (Status, error) {
	if err := validateFence(fence, s.DeploymentID, digest(raw), digest(gate), digest(helper), now); err != nil {
		schema, historicalErr := rootHistoricalProof(s, raw, fence)
		if historicalErr != nil {
			return s, fmt.Errorf("deployment fence: %w; %v", err, historicalErr)
		}
		if schema == "mcpshka.historical-fence-proof/v2" {
			return fencedStatus(s, fence, true), nil
		}
	}
	return fencedStatus(s, fence, false), nil
}

func fencedStatus(s Status, fence []byte, retired bool) Status {
	s.Blocking = false
	s.Fenced = true
	s.FenceSHA256 = digest(fence)
	s.ReconciliationRequired = true
	s.ReasonCode = "FENCED_HISTORICAL_OUTCOME_UNKNOWN"
	s.NextAction = "retain_original_receipt_and_fence_never_replay"
	s.Retired = retired
	if retired {
		s.ReconciliationRequired = false
		s.ReasonCode = "PERMANENTLY_RETIRED_OUTCOME_UNKNOWN"
		s.NextAction = "none_retain_nonreplay_record"
	}
	return s
}
