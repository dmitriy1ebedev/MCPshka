package rootbroker

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	totpSeedFile         = "/etc/mcpshka/totp.secret"
	totpLedgerFile       = "/var/lib/mcpshka-executor/totp-ledger.json"
	totpPeriod     int64 = 30
)

type totpLedger struct {
	Step     int64 `json:"step"`
	Failures int   `json:"failures"`
	Window   int64 `json:"window"`
	Blocked  int64 `json:"blocked"`
}

func validHex(v string, length int) bool {
	if len(v) != length {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func validHash(v string) bool { return validHex(v, 64) }

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	h := hmac.New(sha1.New, secret)
	_, _ = h.Write(msg[:])
	result := h.Sum(nil)
	o := int(result[19] & 15)
	value := binary.BigEndian.Uint32(result[o:o+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

func readTOTPSecret(path string) ([]byte, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, errors.New("TOTP not enrolled; configure locally as root")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || meta.Uid != 0 || meta.Nlink != 1 || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 128 {
		return nil, errors.New("unsafe TOTP secret")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 129))
	if e != nil {
		return nil, e
	}
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(string(raw))))
	if e != nil || len(key) < 20 || len(key) > 64 {
		return nil, errors.New("invalid TOTP secret")
	}
	return key, nil
}

func openRootState(path string) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || meta.Uid != 0 || meta.Nlink != 1 || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		f.Close()
		return nil, errors.New("unsafe root state file")
	}
	if e = syscall.Flock(fd, syscall.LOCK_EX); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}

func saveJSONState(f *os.File, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	raw = append(raw, '\n')
	if e = f.Truncate(0); e != nil {
		return e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return e
	}
	if _, e = f.Write(raw); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	return syncRootParent(f.Name())
}

func consumeTOTP(code string, now time.Time, secretPath, ledgerPath string) error {
	if len(code) != 6 {
		return errors.New("six-digit verification code required")
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return errors.New("six-digit verification code required")
		}
	}
	secret, e := readTOTPSecret(secretPath)
	if e != nil {
		return e
	}
	f, e := openRootState(ledgerPath)
	if e != nil {
		return e
	}
	defer f.Close()
	var st totpLedger
	meta, e := f.Stat()
	if e != nil {
		return e
	}
	if meta.Size() > 0 {
		raw, e := io.ReadAll(io.LimitReader(f, 4097))
		if e != nil || json.Unmarshal(raw, &st) != nil {
			return errors.New("TOTP replay ledger invalid: manual repair required")
		}
	}
	n := now.Unix()
	if st.Blocked > n {
		return errors.New("TOTP locked for repeated errors")
	}
	if st.Window == 0 || n-st.Window > 600 {
		st.Window = n
		st.Failures = 0
	}
	step := n / totpPeriod
	matched := int64(-1)
	for offset := int64(-1); offset <= 1; offset++ {
		s := step + offset
		if s < 1 || s <= st.Step {
			continue
		}
		expected := totpCode(secret, s)
		if subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			matched = s
		}
	}
	if matched < 0 {
		st.Failures++
		if st.Failures >= 5 {
			st.Blocked = n + 600
		}
		if e = saveJSONState(f, st); e != nil {
			return e
		}
		return errors.New("verification code expired, incorrect, or already used")
	}
	st.Step = matched
	st.Failures = 0
	st.Blocked = 0
	return saveJSONState(f, st)
}

func validateTaskFields(id, scope, liveHash, stageHash string) error {
	if !validHex(id, 32) {
		return errors.New("invalid task id")
	}
	if scope != "homeassistant_automations" {
		return errors.New("unsupported root task scope")
	}
	if !validHash(liveHash) || !validHash(stageHash) {
		return errors.New("SHA-256 plan digests required")
	}
	return nil
}

func parseRootTaskArgs(args []string) (string, string, string, string, string, error) {
	if len(args) != 6 || args[0] != "authorize" {
		return "", "", "", "", "", errors.New("invalid authorization request")
	}
	if e := validateTaskFields(args[1], args[2], args[3], args[4]); e != nil {
		return "", "", "", "", "", e
	}
	if _, e := strconv.Atoi(args[5]); e != nil {
		return "", "", "", "", "", errors.New("six-digit verification code required")
	}
	return args[1], args[2], args[3], args[4], args[5], nil
}
