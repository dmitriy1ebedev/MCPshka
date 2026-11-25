package rootbroker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func archiveFixture(t *testing.T) archiveSpec {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s := archiveSpec{Scope: "bootstrap-110", Root: root, Source: filepath.Join(root, "source"), Destination: filepath.Join(root, "archive"), UID: uint32(os.Geteuid()), Files: []archivePin{{Name: "receipt.json", SHA256: shaHex([]byte("receipt")), Mode: 0600}, {Name: "previous", SHA256: shaHex([]byte("rollback")), Mode: 0700}}}
	if err := os.Mkdir(s.Source, 0700); err != nil {
		t.Fatal(err)
	}
	for i, raw := range [][]byte{[]byte("receipt"), []byte("rollback")} {
		if err := os.WriteFile(filepath.Join(s.Source, s.Files[i].Name), raw, s.Files[i].Mode); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestArchiveNoReplacePreservesBytesModesAndOriginals(t *testing.T) {
	s := archiveFixture(t)
	if got, err := s.observe(); err != nil || got != "SOURCE_VERIFIED" {
		t.Fatalf("before: %s %v", got, err)
	}
	if err := s.move(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.observe(); err != nil || got != "ARCHIVED_VERIFIED" {
		t.Fatalf("after: %s %v", got, err)
	}
	if _, err := os.Lstat(s.Source); !os.IsNotExist(err) {
		t.Fatal("source remains")
	}
	for i, want := range []string{"receipt", "rollback"} {
		p := filepath.Join(s.Destination, s.Files[i].Name)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Fatalf("archive changed: %v", err)
		}
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != s.Files[i].Mode {
			t.Fatal("archive mode changed")
		}
	}
	if err := s.move(); err == nil {
		t.Fatal("archive effect replayed")
	}
}

func TestArchiveNamespaceDriftAlwaysHoldsWithoutOverwrite(t *testing.T) {
	for _, kind := range []string{"both", "neither", "changed", "extra", "mode", "special-file", "special-dir", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			s := archiveFixture(t)
			p := filepath.Join(s.Source, s.Files[0].Name)
			var err error
			switch kind {
			case "both":
				err = os.Mkdir(s.Destination, 0700)
			case "neither":
				err = os.Rename(s.Source, s.Source+".preserved")
			case "changed":
				err = os.WriteFile(p, []byte("foreign"), 0600)
			case "extra":
				err = os.WriteFile(filepath.Join(s.Source, "unknown"), []byte("unknown"), 0600)
			case "mode":
				err = os.Chmod(p, 0644)
			case "special-file":
				err = os.Chmod(p, 0600|os.ModeSetuid)
			case "special-dir":
				err = os.Chmod(s.Source, 0700|os.ModeSetgid)
			case "symlink":
				err = os.Rename(p, p+".preserved")
				if err == nil {
					err = os.Symlink(p+".preserved", p)
				}
			case "hardlink":
				err = os.Link(p, filepath.Join(s.Root, "linked"))
			}
			if errors.Is(err, os.ErrPermission) && (kind == "special-file" || kind == "special-dir") {
				// RestrictSUIDSGID sandboxes (e.g. MCPshka jobs) forbid the fixture itself.
				t.Skip("setuid/setgid fixture forbidden by sandbox")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.observe(); err == nil {
				t.Fatal("namespace drift accepted")
			}
			if err := s.move(); err == nil {
				t.Fatal("unverified source moved")
			}
		})
	}
}

func TestArchiveIntentSurvivesReloadAndCannotBeRewritten(t *testing.T) {
	s := archiveFixture(t)
	r := archiveRecord{Schema: "mcpshka.archive/v1", ID: archiveID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "owner", s.Scope), Scope: s.Scope, Session: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Owner: "owner", Generation: 1, CatalogSHA256: s.digest(), Phase: "PREPARED", InitialState: "SOURCE_VERIFIED"}
	if err := s.event(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.load(r.ID)
	if err != nil || got.Phase != "PREPARED" {
		t.Fatalf("reload: %+v %v", got, err)
	}
	r.Phase = "DISPATCHED"
	if err := s.event(r); err != nil {
		t.Fatal(err)
	}
	if err := s.event(r); err == nil {
		t.Fatal("immutable dispatch event overwritten")
	}
	r.Phase = "RUNNING"
	if err := s.event(r); err != nil {
		t.Fatal(err)
	}
	if err := s.move(); err != nil {
		t.Fatal(err)
	}
	got, err = s.load(r.ID)
	if err != nil || got.Phase != "RUNNING" {
		t.Fatal("interrupted worker lost its intent")
	}
	if state, err := s.observe(); err != nil || state != "ARCHIVED_VERIFIED" {
		t.Fatal("crash after rename cannot be reconciled")
	}
	r.Phase = "VERIFIED"
	r.Effect = "archive_effect_reconciled"
	if err := s.event(r); err != nil {
		t.Fatal(err)
	}
	got, err = s.load(r.ID)
	if err != nil || got.Phase != "VERIFIED" {
		t.Fatal("reconciled receipt missing")
	}
	r.CatalogSHA256 = shaHex([]byte("changed catalogue"))
	r.Phase = "HELD"
	if err := s.event(r); err == nil {
		t.Fatal("changed catalogue admitted")
	}
}

func TestArchiveOperationBindsOwnerSessionAndScope(t *testing.T) {
	a := archiveID("session", "owner", "scope")
	for _, b := range []string{archiveID("other", "owner", "scope"), archiveID("session", "other", "scope"), archiveID("session", "owner", "other")} {
		if a == b {
			t.Fatal("archive identity lost its authority binding")
		}
	}
}
