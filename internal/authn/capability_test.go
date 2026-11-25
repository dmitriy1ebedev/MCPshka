package authn

import (
	"path/filepath"
	"testing"
)

func TestRegistryCapabilityDefaultsAndRestrictions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeRegistry(t, path, Registry{
		Version: 1,
		Credentials: []Credential{
			{ID: "default-operator", TokenSHA256: HashToken("default-secret")},
			{ID: "reader", TokenSHA256: HashToken("reader-secret"), Role: "reader", Scopes: []string{"device_status", "fs_read"}},
		},
	})
	auth, err := New(path, "")
	if err != nil {
		t.Fatal(err)
	}

	def, err := auth.Authenticate("default-secret")
	if err != nil {
		t.Fatal(err)
	}
	if def.Role != "operator" || len(def.Scopes) != 1 || def.Scopes[0] != "*" {
		t.Fatalf("default capabilities=%+v", def)
	}

	reader, err := auth.Authenticate("reader-secret")
	if err != nil {
		t.Fatal(err)
	}
	if reader.Role != "reader" || len(reader.Scopes) != 2 || reader.Scopes[0] != "device_status" || reader.Scopes[1] != "fs_read" {
		t.Fatalf("reader capabilities=%+v", reader)
	}
}

func TestRegistryRejectsInvalidCapabilityPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeRegistry(t, path, Registry{
		Version: 1,
		Credentials: []Credential{{
			ID: "bad-role", TokenSHA256: HashToken("secret"), Role: "admin",
		}},
	})
	if _, err := New(path, ""); err == nil {
		t.Fatal("unsupported role accepted")
	}

	writeRegistry(t, path, Registry{
		Version: 1,
		Credentials: []Credential{{
			ID: "bad-scope", TokenSHA256: HashToken("secret"), Role: "reader", Scopes: []string{"fs read"},
		}},
	})
	if _, err := New(path, ""); err == nil {
		t.Fatal("invalid scope accepted")
	}
}
