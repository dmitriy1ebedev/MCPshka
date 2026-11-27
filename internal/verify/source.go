package verify

import (
	"encoding/hex"
	"errors"
)

type SourceProof struct {
	Schema           string `json:"schema"`
	DeploymentID     string `json:"deployment_id"`
	SourceSHA        string `json:"source_sha"`
	Tree             string `json:"tree"`
	SourceDigest     string `json:"source_digest"`
	Version          string `json:"version"`
	CIRun            int64  `json:"ci_run"`
	ControllerSHA256 string `json:"controller_sha256"`
	BrokerSHA256     string `json:"broker_sha256"`
	CIReceiptSHA256  string `json:"ci_receipt_sha256"`
}

// BindSource checks actual running images as well as the signed installed images.
func BindSource(p SourceProof, deployment, version, tree, controller, brokerVersion, broker string) error {
	valid := func(value string, size int) bool {
		raw, err := hex.DecodeString(value)
		return err == nil && len(raw) == size && hex.EncodeToString(raw) == value
	}
	if p.Schema != "mcpshka.production-source/v1" || p.DeploymentID != deployment ||
		p.Version != version || brokerVersion != version || p.Tree != tree ||
		p.ControllerSHA256 != controller || p.BrokerSHA256 != broker || p.CIRun <= 0 ||
		!valid(p.SourceSHA, 20) || !valid(p.Tree, 20) || !valid(p.SourceDigest, 32) ||
		!valid(p.ControllerSHA256, 32) || !valid(p.BrokerSHA256, 32) || !valid(p.CIReceiptSHA256, 32) {
		return errors.New("production source, CI or running paired images mismatch: HOLD")
	}
	return nil
}
