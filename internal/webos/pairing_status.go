package webos

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// PairingStatus exposes only the LG Developer Mode key's existence, security
// posture and the pinned public host-key fingerprint (MCP-196). It does not
// read private-key bytes, change known_hosts, log in or execute a TV command.
type PairingStatus struct {
	Alias             string   `json:"alias"`
	KeyPresent        bool     `json:"key_present"`
	KeyPermissionsOK  bool     `json:"key_permissions_ok"`
	HostKeyPinned     bool     `json:"host_key_pinned"`
	HostKeyType       string   `json:"host_key_type,omitempty"`
	HostFingerprint   string   `json:"host_fingerprint,omitempty"`
	Reachability      string   `json:"reachability"`
	SSHAuthentication string   `json:"ssh_authentication"`
	State             string   `json:"state"`
	Reason            []string `json:"reason,omitempty"`
	ActualMutation    bool     `json:"actual_mutation"`
}

// openPairingFD holds the same inode from permission check through read.
// O_NOFOLLOW rejects a symlink swapped between inspection and file open.
func openPairingFD(path string) (*os.File, os.FileInfo, bool) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, false
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, nil, false
	}
	return f, info, true
}

func securePairingFile(path string) (os.FileInfo, bool) {
	f, info, ok := openPairingFD(path)
	if ok {
		_ = f.Close()
	}
	return info, ok
}

func pinnedLGFingerprint(path string) (string, string, bool) {
	f, info, secure := openPairingFD(path)
	if !secure {
		return "", "", false
	}
	defer f.Close()
	if info.Size() < 1 || info.Size() > 64<<10 {
		return "", "", false
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", "", false
	}
	var kind, fingerprint string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		aliasField := fields[0]
		marked := strings.HasPrefix(aliasField, "@")
		if marked {
			aliasField = fields[1]
		}
		pinnedAlias := false
		for _, alias := range strings.Split(aliasField, ",") {
			if alias == HostKeyAlias {
				pinnedAlias = true
			}
		}
		if !pinnedAlias {
			continue
		}
		if marked {
			return "", "", false
		}
		switch fields[1] {
		case "ssh-rsa", "ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		default:
			return "", "", false
		}
		blob, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil || len(blob) < 4 || len(blob) > 8192 {
			return "", "", false
		}
		key, err := ssh.ParsePublicKey(blob)
		if err != nil || key.Type() != fields[1] {
			return "", "", false
		}
		next := ssh.FingerprintSHA256(key)
		if fingerprint != "" && (fingerprint != next || kind != fields[1]) {
			return "", "", false // ambiguous host pin: no automatic repair
		}
		kind, fingerprint = fields[1], next
	}
	return kind, fingerprint, fingerprint != ""
}

// InspectPairing is side-effect free. An optional RFC1918 IPv4 TV address is
// probed only on the fixed Developer Mode TCP 9922 port. An open TCP port is
// NOT proof of SSH authentication; that requires a separate scoped challenge.
func InspectPairing(ctx context.Context, tvIP string) PairingStatus {
	return inspectPairing(ctx, KeyDir, tvIP)
}

func inspectPairing(ctx context.Context, keyDir, tvIP string) PairingStatus {
	out := PairingStatus{
		Alias: HostKeyAlias, Reachability: "NOT_CHECKED",
		SSHAuthentication: "UNVERIFIED", State: "HOLD",
		ActualMutation: false,
	}
	folder, err := os.Lstat(keyDir)
	if err != nil || !folder.IsDir() || folder.Mode().Perm()&0077 != 0 {
		out.Reason = append(out.Reason, "PAIRING_DIRECTORY_UNSAFE_OR_MISSING")
		return out
	}
	key, secure := securePairingFile(filepath.Join(keyDir, "prisoner_key"))
	out.KeyPresent = key != nil && key.Size() > 0 && secure
	out.KeyPermissionsOK = secure
	if !out.KeyPresent {
		out.Reason = append(out.Reason, "PAIRING_KEY_UNAVAILABLE")
	}
	out.HostKeyType, out.HostFingerprint, out.HostKeyPinned =
		pinnedLGFingerprint(filepath.Join(keyDir, "known_hosts"))
	if !out.HostKeyPinned {
		out.Reason = append(out.Reason, "HOST_KEY_NOT_PINNED")
	}
	if tvIP != "" {
		ip := net.ParseIP(tvIP)
		if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
			out.Reachability = "UNSUPPORTED_ADDRESS"
			out.Reason = append(out.Reason, "TV_IP_MUST_BE_PRIVATE_IPV4")
		} else if out.KeyPresent && out.HostKeyPinned {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(probeCtx, "tcp", net.JoinHostPort(ip.String(), "9922"))
			if err != nil {
				out.Reachability = "UNREACHABLE"
			} else {
				out.Reachability = "PORT_OPEN"
				_ = conn.Close()
			}
		}
	}
	if out.KeyPresent && out.HostKeyPinned && (tvIP == "" || out.Reachability == "PORT_OPEN") {
		out.State = "CONFIGURED_UNVERIFIED"
	}
	return out
}
