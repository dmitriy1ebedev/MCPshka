package rootbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/transientunit"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var validBareVersion = transientunit.ValidVersion

func validateDiagTraffic(args []string) error {
	if len(args) == 0 {
		return errors.New("missing diag traffic operation")
	}
	switch args[0] {
	case "start":
		if len(args) != 9 ||
			args[1] != "--client" ||
			args[3] != "--mac" ||
			args[5] != "--duration" ||
			args[7] != "--root" ||
			filepath.Clean(args[8]) != diagTrafficRunsRoot {
			return errors.New("unsupported diag traffic start form")
		}
		if strings.Contains(args[2], ":") {
			return errors.New("diag traffic client must use dotted IPv4 form")
		}
		ip := net.ParseIP(args[2])
		if ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() || ip.IsMulticast() || ip.IsUnspecified() {
			return errors.New("diag traffic client must be private unicast IPv4")
		}
		if len(args[4]) != 17 || strings.Count(args[4], ":") != 5 {
			return errors.New("invalid diag traffic MAC")
		}
		mac, err := net.ParseMAC(args[4])
		if err != nil || len(mac) != 6 || mac[0]&1 != 0 || bytes.Count(mac, []byte{0}) == 6 {
			return errors.New("invalid diag traffic MAC")
		}
		duration, err := strconv.Atoi(args[6])
		if err != nil || duration < 10 || duration > 300 {
			return errors.New("diag traffic duration must be 10..300 seconds")
		}
		return nil
	case "status", "report", "stop":
		if len(args) != 6 ||
			args[1] != "--run" ||
			!validDiagTrafficRunID(args[2]) ||
			args[3] != "--root" ||
			filepath.Clean(args[4]) != diagTrafficRunsRoot ||
			args[5] != "--json" {
			return errors.New("unsupported diag traffic read form")
		}
		return nil
	default:
		return fmt.Errorf("unsupported diag traffic operation %q", args[0])
	}
}

func validDiagTrafficRunID(v string) bool {
	if len(v) != 25 || v[8] != 'T' || v[15] != 'Z' || v[16] != '-' {
		return false
	}
	for i := 0; i < 16; i++ {
		if i == 8 || i == 15 {
			continue
		}
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	for i := 17; i < len(v); i++ {
		if !((v[i] >= '0' && v[i] <= '9') || (v[i] >= 'a' && v[i] <= 'f')) {
			return false
		}
	}
	return true
}

func validateRootOwnedExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("helper must be an executable regular non-symlink file")
	}
	if info.Mode().Perm()&0022 != 0 {
		return errors.New("helper must not be group/world writable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("helper must be owned by root")
	}
	return nil
}

// validateRootOwnedTool accepts a tool installed as a symlink into a
// versioned directory (/usr/local/bin/rclone -> diag-tools/rclone-1.75.1):
// the link, its target and every directory on the target's path must be
// root-owned and not group/world writable, so no one else can swap it.
func validateRootOwnedTool(path string) error {
	if err := rootOwnedNotWritable(path, true); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if err := validateRootOwnedExecutable(real); err != nil {
		return err
	}
	for dir := filepath.Dir(real); ; dir = filepath.Dir(dir) {
		if err := rootOwnedNotWritable(dir, false); err != nil {
			return err
		}
		if dir == "/" {
			return nil
		}
	}
}

func rootOwnedNotWritable(path string, link bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New(path + " must be owned by root")
	}
	// A symlink's own mode bits are meaningless; directories and files must
	// not be writable by group or others.
	if !(link && info.Mode()&os.ModeSymlink != 0) && info.Mode().Perm()&0022 != 0 {
		return errors.New(path + " must not be group/world writable")
	}
	return nil
}

func validateStateBackup(args []string) error {
	if len(args) != 2 || len(args[1]) != 32 {
		return errors.New("invalid state backup operation")
	}
	switch args[0] {
	case "start", "probe", "restore":
	default:
		return errors.New("unsupported state backup action")
	}
	for _, c := range args[1] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("invalid backup identity")
		}
	}
	return nil
}

func validateCodexBridge(args []string) error {
	if len(args) != 2 || len(args[1]) > 12000 || !json.Valid([]byte(args[1])) {
		return errors.New("invalid Codex request")
	}
	switch args[0] {
	case "sessions_list", "session_read", "task_submit", "task_status", "task_cancel":
	default:
		return errors.New("unsupported Codex action")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args[1]), &fields); err != nil || fields == nil {
		return errors.New("Codex object required")
	}
	return nil
}

func validateRescue(args []string) error {
	if len(args) == 1 && args[0] == "status" {
		return nil
	}
	var id string
	if len(args) == 2 && args[0] == "status" {
		id = args[1]
	} else if len(args) == 3 && args[0] == "start" {
		switch args[1] {
		case "reconnect", "restart", "rollback":
		default:
			return errors.New("unsupported rescue mode")
		}
		id = args[2]
	} else {
		return errors.New("unsupported rescue request")
	}
	if len(id) != 32 {
		return errors.New("invalid rescue identity")
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("invalid rescue identity")
		}
	}
	return nil
}
