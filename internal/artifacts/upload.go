package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"mcpshka/internal/statestore"
)

const (
	MaxUploadSize  = int64(16 * 1024 * 1024)
	MaxUploadChunk = 24 * 1024
	MaxInboxBytes  = int64(64 * 1024 * 1024)
	MaxInboxFiles  = 32
	uploadPrefix   = "artifact_uploads/v1/"
)

type Upload struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Offset    int64     `json:"offset"`
	State     string    `json:"state"`
	Trust     string    `json:"trust"`
	Source    string    `json:"source"`
	Path      string    `json:"path,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}
type pendingChunk struct {
	Offset int64
	Size   int
	SHA256 string
}
type uploadRecord struct {
	Upload
	Owner   string
	Pending *pendingChunk `json:",omitempty"`
}
type Inbox struct {
	mu  sync.Mutex
	dir string
	db  *statestore.DB
	now func() time.Time
}

func NewInbox(dir string, db *statestore.DB) *Inbox {
	return &Inbox{dir: dir, db: db, now: time.Now}
}
func validHex(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && hex.EncodeToString(b) == s
}
func validUploadName(s string) bool {
	return s != "" && len(s) <= 200 && utf8.ValidString(s) && s != "." && s != ".." && !strings.ContainsAny(s, "/\\") && !strings.ContainsFunc(s, unicode.IsControl)
}
func privateUploadFile(st os.FileInfo) bool {
	info, ok := st.Sys().(*syscall.Stat_t)
	return ok && info.Uid == uint32(os.Geteuid()) && info.Nlink == 1 && st.Mode().IsRegular() && st.Mode().Perm()&0077 == 0
}
func syncInbox(root *os.Root) error {
	d, e := root.Open(".")
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func diskReserveOK(total, free, needed uint64) bool {
	reserve := uint64(3 << 30)
	if total/10 > reserve {
		reserve = total / 10
	}
	return free >= reserve && needed <= free-reserve
}

var inboxStatfs = syscall.Fstatfs

func checkInboxDisk(root *os.Root, needed int64) error {
	d, e := root.Open(".")
	if e != nil {
		return e
	}
	defer d.Close()
	var fs syscall.Statfs_t
	if e = inboxStatfs(int(d.Fd()), &fs); e != nil {
		return e
	}
	if needed < 0 || !diskReserveOK(uint64(fs.Blocks)*uint64(fs.Bsize), uint64(fs.Bavail)*uint64(fs.Bsize), uint64(needed)) {
		return errors.New("artifact inbox disk reserve insufficient")
	}
	return nil
}
func (i *Inbox) locked(fn func(*os.Root) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.db == nil {
		return errors.New("artifact inbox requires SQLite authority")
	}
	if e := os.MkdirAll(i.dir, 0700); e != nil {
		return e
	}
	st, e := os.Lstat(i.dir)
	if e != nil {
		return e
	}
	owner, owned := st.Sys().(*syscall.Stat_t)
	if !owned || owner.Uid != uint32(os.Geteuid()) || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe inbox directory")
	}
	root, e := os.OpenRoot(i.dir)
	if e != nil {
		return e
	}
	defer root.Close()
	f, e := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	lockInfo, e := f.Stat()
	if e != nil || !privateUploadFile(lockInfo) {
		return errors.New("unsafe inbox lock")
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("artifact inbox busy")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn(root)
}
func (i *Inbox) save(r uploadRecord) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return i.db.Put(uploadPrefix+r.ID, b)
}
func (i *Inbox) load(owner, id string) (uploadRecord, error) {
	var r uploadRecord
	if !validHex(id, 16) || owner == "" {
		return r, ErrNotFound
	}
	b, e := i.db.Get(uploadPrefix + id)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.ID != id || r.Owner != owner {
		return uploadRecord{}, ErrNotFound
	}
	if r.Size < 0 || r.Size > MaxUploadSize || r.Offset < 0 || r.Offset > r.Size || !validHex(r.SHA256, 32) {
		return uploadRecord{}, errors.New("invalid upload record: HOLD")
	}
	return r, nil
}
func (i *Inbox) Begin(owner, key, name string, size int64, digest string) (Upload, error) {
	var out Upload
	if owner == "" || len(owner) > 256 || key == "" || len(key) > 256 || !validUploadName(name) || size < 0 || size > MaxUploadSize || !validHex(digest, 32) {
		return out, errors.New("invalid upload name, size, SHA-256 or retry key")
	}
	sum := sha256.Sum256([]byte(owner + "\x00" + key))
	id := hex.EncodeToString(sum[:16])
	e := i.locked(func(root *os.Root) error {
		r, e := i.load(owner, id)
		if e == nil {
			if r.Name != name || r.Size != size || r.SHA256 != digest {
				return errors.New("upload retry conflict")
			}
			out = r.Upload
			return nil
		}
		if !os.IsNotExist(e) {
			return e
		}
		var total, remaining int64
		count := 0
		e = i.db.Scan(uploadPrefix, func(_ string, b []byte) error {
			var existing uploadRecord
			if e := json.Unmarshal(b, &existing); e != nil {
				return e
			}
			if !validHex(existing.ID, 16) || existing.Size < 0 || existing.Size > MaxUploadSize {
				return errors.New("invalid inbox quota record: HOLD")
			}
			if existing.State != "cancelled" {
				total += existing.Size
				if existing.Offset < 0 || existing.Offset > existing.Size {
					return errors.New("invalid inbox reservation: HOLD")
				}
				remaining += existing.Size - existing.Offset
				count++
			}
			if total > MaxInboxBytes || count > MaxInboxFiles {
				return errors.New("inbox quota: HOLD")
			}
			return nil
		})
		if e != nil {
			return e
		}
		if total+size > MaxInboxBytes || count >= MaxInboxFiles {
			return errors.New("artifact inbox quota exceeded; review or cancel incomplete uploads")
		}
		if e = checkInboxDisk(root, remaining+size); e != nil {
			return e
		}
		// Count orphan files too: interrupted allocation cannot bypass the budget.
		d, e := root.Open(".")
		if e != nil {
			return e
		}
		entries, e := d.ReadDir(MaxInboxFiles + 2)
		d.Close()
		if e != nil && e != io.EOF {
			return e
		}
		if len(entries) > MaxInboxFiles {
			return errors.New("inbox file quota exceeded")
		}
		f, e := root.OpenFile(id+".blob", os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return fmt.Errorf("upload allocation uncertain: HOLD: %w", e)
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return e
		}
		f.Close()
		if e = syncInbox(root); e != nil {
			return e
		}
		r = uploadRecord{Upload: Upload{ID: id, Name: name, Size: size, SHA256: digest, State: "receiving", Trust: "UNTRUSTED", Source: "chat-mcp", ExpiresAt: i.now().UTC().Add(24 * time.Hour)}, Owner: owner}
		if e = i.save(r); e != nil {
			return e
		}
		out = r.Upload
		return nil
	})
	return out, e
}
func (i *Inbox) open(root *os.Root, r uploadRecord) (*os.File, error) {
	f, e := root.OpenFile(r.ID+".blob", os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil || !privateUploadFile(st) {
		f.Close()
		return nil, errors.New("unsafe upload file: HOLD")
	}
	return f, nil
}
func fileDigest(f *os.File, offset int64, size int64) (string, error) {
	h := sha256.New()
	n, e := io.Copy(h, io.NewSectionReader(f, offset, size))
	if e != nil {
		return "", e
	}
	if n != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// A durable intent distinguishes a known interrupted chunk from unexplained data.
func (i *Inbox) reconcile(f *os.File, r *uploadRecord) error {
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if r.Pending != nil {
		p := r.Pending
		if p.Offset != r.Offset || p.Size <= 0 || p.Size > MaxUploadChunk || p.Offset+int64(p.Size) > r.Size || !validHex(p.SHA256, 32) {
			return errors.New("invalid chunk intent: HOLD")
		}
		if st.Size() < p.Offset || st.Size() > p.Offset+int64(p.Size) {
			return errors.New("unexplained upload bytes: HOLD")
		}
		if st.Size() == p.Offset+int64(p.Size) {
			digest, e := fileDigest(f, p.Offset, int64(p.Size))
			if e != nil {
				return e
			}
			if digest != p.SHA256 {
				return errors.New("interrupted chunk checksum mismatch: HOLD")
			}
			if e = f.Sync(); e != nil {
				return e
			}
			r.Offset += int64(p.Size)
		} else {
			// Only our proven incomplete tail is discarded, never a completed artifact.
			if e = f.Truncate(p.Offset); e != nil {
				return e
			}
			if e = f.Sync(); e != nil {
				return e
			}
		}
		r.Pending = nil
		return i.save(*r)
	}
	if st.Size() != r.Offset {
		return errors.New("upload offset mismatch: HOLD")
	}
	return nil
}
func (i *Inbox) Status(owner, id string) (Upload, error) {
	var out Upload
	e := i.locked(func(root *os.Root) error {
		r, e := i.load(owner, id)
		if e != nil {
			return e
		}
		out = r.Upload
		if r.State == "cancelled" {
			return nil
		}
		f, e := i.open(root, r)
		if r.State == "cancelling" && os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil {
			return e
		}
		if r.Pending != nil || st.Size() != r.Offset {
			out.State = "reconcile_required"
		}
		if r.State == "ready" {
			digest, e := fileDigest(f, 0, r.Size)
			if e != nil {
				return e
			}
			if digest != r.SHA256 {
				return ErrChanged
			}
		}
		if !i.now().Before(r.ExpiresAt) && r.State == "receiving" {
			out.State = "expired"
		}
		return nil
	})
	return out, e
}
func (i *Inbox) Chunk(owner, id string, offset int64, data []byte) (Upload, error) {
	var out Upload
	if offset < 0 || len(data) == 0 || len(data) > MaxUploadChunk {
		return out, errors.New("chunk size or offset invalid")
	}
	e := i.locked(func(root *os.Root) error {
		r, e := i.load(owner, id)
		if e != nil {
			return e
		}
		if r.State != "receiving" || !i.now().Before(r.ExpiresAt) {
			return errors.New("upload not receiving")
		}
		f, e := i.open(root, r)
		if e != nil {
			return e
		}
		defer f.Close()
		if e = i.reconcile(f, &r); e != nil {
			return e
		}
		end := offset + int64(len(data))
		if end > r.Size {
			return errors.New("chunk exceeds declared size")
		}
		if offset < r.Offset && end <= r.Offset {
			old := make([]byte, len(data))
			if _, e = f.ReadAt(old, offset); e != nil {
				return e
			}
			if !bytes.Equal(old, data) {
				return errors.New("chunk retry content conflict")
			}
			out = r.Upload
			return nil
		}
		if offset != r.Offset {
			return errors.New("chunk offset conflict; probe artifact_upload_status")
		}
		if e = checkInboxDisk(root, int64(len(data))); e != nil {
			return e
		}
		sum := sha256.Sum256(data)
		r.Pending = &pendingChunk{offset, len(data), hex.EncodeToString(sum[:])}
		if e = i.save(r); e != nil {
			return e
		}
		if _, e = f.WriteAt(data, offset); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		r.Offset = end
		r.Pending = nil
		if e = i.save(r); e != nil {
			return e
		}
		out = r.Upload
		return nil
	})
	return out, e
}
func (i *Inbox) Finish(owner, id string) (Upload, error) {
	var out Upload
	e := i.locked(func(root *os.Root) error {
		r, e := i.load(owner, id)
		if e != nil {
			return e
		}
		if r.State != "receiving" && r.State != "ready" {
			return errors.New("upload not finishable")
		}
		if r.State == "receiving" && !i.now().Before(r.ExpiresAt) {
			return errors.New("upload expired")
		}
		f, e := i.open(root, r)
		if e != nil {
			return e
		}
		defer f.Close()
		if e = i.reconcile(f, &r); e != nil {
			return e
		}
		if r.Offset != r.Size {
			return errors.New("upload incomplete")
		}
		digest, e := fileDigest(f, 0, r.Size)
		if e != nil {
			return e
		}
		if digest != r.SHA256 {
			return errors.New("artifact SHA-256 mismatch")
		}
		r.State = "ready"
		r.Path = filepath.Join(i.dir, r.ID+".blob")
		if e = i.save(r); e != nil {
			return e
		}
		out = r.Upload
		return nil
	})
	return out, e
}
func (i *Inbox) Cancel(owner, id string) (Upload, error) {
	var out Upload
	e := i.locked(func(root *os.Root) error {
		r, e := i.load(owner, id)
		if e != nil {
			return e
		}
		if r.State == "ready" {
			return errors.New("completed artifact requires explicit review; cancellation cannot delete it")
		}
		// Keep its reservation until deletion is durable.
		r.State = "cancelling"
		r.Pending = nil
		r.Path = ""
		if e = i.save(r); e != nil {
			return e
		}
		if e = root.Remove(r.ID + ".blob"); e != nil && !os.IsNotExist(e) {
			return e
		}
		if e = syncInbox(root); e != nil {
			return e
		}
		r.State = "cancelled"
		if e = i.save(r); e != nil {
			return e
		}
		out = r.Upload
		return nil
	})
	return out, e
}
