package rootbroker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type archivePin struct {
	Name   string
	SHA256 string
	Mode   os.FileMode
}

type archiveSpec struct {
	Scope, Root, Source, Destination string
	UID                              uint32
	Files                            []archivePin
}

func bootstrapArchive() archiveSpec {
	return archiveSpec{Scope: "bootstrap-110", Root: taskRoot,
		Source:      "/var/lib/mcpshka-release-gates/bootstrap-0.1.110-20261015",
		Destination: taskRoot + "/bootstrap-0.1.110-20261015", UID: 0,
		Files: []archivePin{
			{Name: "receipt.json", SHA256: "5740579dc8094c4bcb683e5188db5b5293ec0987bcbaf4eda66ad0f36e7bd1d5", Mode: 0600},
			{Name: "mcpshka-self-deploy.previous", SHA256: "182f5b61baa0eaa707bd8d99f079061c2cd6dda1f4aeccb101d43085a3da1195", Mode: 0700},
		}}
}

func (s archiveSpec) digest() string { raw, _ := json.Marshal(s); return shaHex(raw) }

func archiveID(session, owner, scope string) string {
	raw, _ := json.Marshal([]string{session, owner, scope})
	return shaHex(raw)[:32]
}

func archiveDirectory(path string, uid uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path || info.Mode() != os.ModeDir|0700 || !ok || st.Uid != uid {
		return errors.New("unsafe archive directory: HOLD")
	}
	return nil
}

func (s archiveSpec) inventory(path string) error {
	if err := archiveDirectory(path, s.UID); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != len(s.Files) {
		return errors.New("archive inventory changed: HOLD")
	}
	for _, pin := range s.Files {
		name := filepath.Join(path, pin.Name)
		raw, info, err := readFixedRegular(name)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != s.UID || st.Nlink != 1 || info.Mode() != pin.Mode || len(raw) == 0 || len(raw) >= 16384 || shaHex(raw) != pin.SHA256 {
			return errors.New("archive bytes, owner or mode changed: HOLD")
		}
		after, err := os.Lstat(name)
		if err != nil {
			return err
		}
		last, ok := after.Sys().(*syscall.Stat_t)
		if !ok || !os.SameFile(info, after) || info.Size() != after.Size() || info.Mode() != after.Mode() || info.ModTime() != after.ModTime() || st.Ctim != last.Ctim {
			return errors.New("archive file changed while inspected: HOLD")
		}
	}
	return nil
}

func archiveExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (s archiveSpec) observe() (string, error) {
	for _, parent := range []string{filepath.Dir(s.Source), filepath.Dir(s.Destination)} {
		if err := archiveDirectory(parent, s.UID); err != nil {
			return "", err
		}
	}
	source, err := archiveExists(s.Source)
	if err != nil {
		return "", err
	}
	destination, err := archiveExists(s.Destination)
	if err != nil {
		return "", err
	}
	if source == destination {
		return "", errors.New("archive source/destination conflict: HOLD")
	}
	path, state := s.Source, "SOURCE_VERIFIED"
	if destination {
		path, state = s.Destination, "ARCHIVED_VERIFIED"
	}
	if err := s.inventory(path); err != nil {
		return "", err
	}
	return state, nil
}

func (s archiveSpec) move() error {
	state, err := s.observe()
	if err != nil {
		return err
	}
	if state != "SOURCE_VERIFIED" {
		return errors.New("archive effect already observed; do not replay")
	}
	if err := unix.Renameat2(unix.AT_FDCWD, s.Source, unix.AT_FDCWD, s.Destination, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	for _, path := range []string{s.Source, s.Destination} {
		if err := syncRootParent(path); err != nil {
			return err
		}
	}
	state, err = s.observe()
	if err != nil {
		return err
	}
	if state != "ARCHIVED_VERIFIED" {
		return errors.New("archive effect unconfirmed: UNKNOWN")
	}
	return nil
}

type archiveRecord struct {
	Schema        string `json:"schema"`
	ID            string `json:"id"`
	Scope         string `json:"scope"`
	Session       string `json:"session_id"`
	Owner         string `json:"owner"`
	Generation    int64  `json:"generation"`
	CatalogSHA256 string `json:"catalog_sha256"`
	Phase         string `json:"phase"`
	InitialState  string `json:"initial_state"`
	At            string `json:"at"`
	Effect        string `json:"effect"`
}

var archivePhases = []string{"PREPARED", "DISPATCHED", "RUNNING", "HELD", "VERIFIED"}

func (s archiveSpec) eventPath(id, phase string) string {
	return filepath.Join(s.Root, "archive-"+id+"-"+phase+".json")
}

func (s archiveSpec) validate(r archiveRecord) error {
	known := false
	for _, phase := range archivePhases {
		known = known || r.Phase == phase
	}
	at, err := time.Parse(time.RFC3339Nano, r.At)
	if !known || r.Schema != "mcpshka.archive/v1" || !validHex(r.Session, 32) || !safeOwner(r.Owner) || r.Owner == "local-stdio" || r.Scope != s.Scope || r.ID != archiveID(r.Session, r.Owner, r.Scope) || r.CatalogSHA256 != s.digest() || r.Generation <= 0 || r.Generation >= 1<<53 || (r.InitialState != "SOURCE_VERIFIED" && r.InitialState != "ARCHIVED_VERIFIED") || err != nil || at.IsZero() {
		return errors.New("archive record binding changed: HOLD")
	}
	if (r.Phase != "VERIFIED" && r.Effect != "") || (r.Phase == "VERIFIED" && r.Effect != "archive_move_verified" && r.Effect != "observed_existing_archive" && r.Effect != "archive_effect_reconciled") {
		return errors.New("archive effect attribution invalid: HOLD")
	}
	return nil
}

func (s archiveSpec) event(r archiveRecord) error {
	r.At = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.validate(r); err != nil {
		return err
	}
	if err := archiveDirectory(s.Root, s.UID); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Root, ".archive-event-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	path := s.eventPath(r.ID, r.Phase)
	if err := unix.Renameat2(unix.AT_FDCWD, f.Name(), unix.AT_FDCWD, path, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return syncRootParent(path)
}

func (s archiveSpec) load(id string) (archiveRecord, error) {
	var latest archiveRecord
	if !validHex(id, 32) {
		return latest, errors.New("invalid archive identity")
	}
	if err := archiveDirectory(s.Root, s.UID); err != nil {
		return latest, err
	}
	dispatched := false
	for i, phase := range archivePhases {
		raw, info, err := readFixedRegular(s.eventPath(id, phase))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return latest, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != s.UID || st.Nlink != 1 || info.Mode() != 0600 {
			return latest, errors.New("unsafe archive event: HOLD")
		}
		var r archiveRecord
		if err := decodeUniqueHistorical(raw, &r, 11); err != nil {
			return latest, err
		}
		if err := s.validate(r); err != nil {
			return latest, err
		}
		if r.ID != id || r.Phase != phase || (i > 0 && latest.ID == "") || (i > 1 && !dispatched) {
			return latest, errors.New("archive event chain incomplete: HOLD")
		}
		if latest.ID != "" && (latest.Session != r.Session || latest.Owner != r.Owner || latest.Generation != r.Generation || latest.InitialState != r.InitialState) {
			return latest, errors.New("archive event authority changed: HOLD")
		}
		dispatched = dispatched || phase == "DISPATCHED"
		latest = r
	}
	if latest.ID == "" {
		return latest, os.ErrNotExist
	}
	return latest, nil
}
