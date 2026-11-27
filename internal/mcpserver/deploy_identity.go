package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"mcpshka/internal/idempotency"
)

// The versioned operation distinguishes reserved identities from legacy random IDs.
const reservedDeployOperation = "project_deploy/reserved-v1"

func reservedDeploymentID(key string, receipt idempotency.Status) (string, error) {
	if strings.TrimSpace(key) == "" || receipt.Operation != reservedDeployOperation || receipt.StartedAt.IsZero() || len(receipt.Fingerprint) != 64 {
		return "", errors.New("deployment identity is not durably reserved: HOLD")
	}
	if _, err := hex.DecodeString(receipt.Fingerprint); err != nil {
		return "", errors.New("invalid deployment reservation fingerprint")
	}
	timestamp := fmt.Sprint(receipt.StartedAt.UnixMilli())
	if len(timestamp) != 13 {
		return "", errors.New("unsupported deployment reservation time")
	}
	for _, digit := range timestamp {
		if digit < '0' || digit > '9' {
			return "", errors.New("unsupported deployment reservation time")
		}
	}
	hash := sha256.Sum256([]byte(reservedDeployOperation + "\x00" + strings.TrimSpace(key) + "\x00" + receipt.Fingerprint))
	return timestamp + "-" + hex.EncodeToString(hash[:6]), nil
}
