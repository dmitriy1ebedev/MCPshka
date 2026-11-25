package knowledge

import (
	"path"
	"strings"
)

// Recovery knowledge contains local manifest references, never keys or cloud bytes.
func validRecoveryReferences(r Record) error {
	if r.Kind != "backup_manifest" && r.Kind != "restore_recipe" {
		return nil
	}
	if r.Run == nil {
		return ErrInvalid
	}
	for key, field := range r.Fields {
		if key != "manifest_ref" && key != "recipe_ref" {
			return ErrInvalid
		}
		value := strings.TrimPrefix(field.Value, "state:")
		if value == field.Value || !strings.HasPrefix(value, "backups/") || path.Clean(value) != value || strings.Contains(value, "..") || len(value) > 256 || looksCredential("", value) {
			return ErrInvalid
		}
	}
	return nil
}
