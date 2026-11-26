// Package nodes manages the SSH nodes MCPshka reaches (VPS stands, the
// laptop runner): add one with a fresh MCPshka key, compare the host key the
// node presents with the pinned one, accept a changed key only by the exact
// fingerprint the owner saw, check the link, remove the node (MCP-147).
//
// State is the key directory itself: <name> and <name>.pub are the client
// key, <name>.known_hosts the pinned host key, <name>.json the address. A
// legacy node without .json takes its address from /etc/mcpshka/oob.json.
package nodes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dir holds the node keys; OOBConfig names legacy addresses.
var (
	Dir       = "/var/lib/mcpshka/ssh"
	OOBConfig = "/etc/mcpshka/oob.json"
)

// Tools are fixed paths; tests swap them for fakes.
var (
	sshKeygen  = "/usr/bin/ssh-keygen"
	sshKeyscan = "/usr/bin/ssh-keyscan"
	sshClient  = "/usr/bin/ssh"
)

var (
	validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	validUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// Addr is where a node listens.
type Addr struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
}

func (a Addr) hostPattern() string {
	if a.Port == 22 {
		return a.Host
	}
	return "[" + a.Host + "]:" + strconv.Itoa(a.Port)
}

// Node is one entry as the owner sees it.
type Node struct {
	Name        string      `json:"name"`
	Addr        *Addr       `json:"addr,omitempty"`
	PublicKey   string      `json:"public_key,omitempty"` // line for authorized_keys
	Pinned      []string    `json:"pinned,omitempty"`     // SHA256 fingerprints in known_hosts
	AddressNote string      `json:"address_note,omitempty"`
	LastCheck   *Probe      `json:"last_check,omitempty"`
	LastInspect *Inspection `json:"last_inspect,omitempty"`
}

// Probe is the last «Проверить связь» result: the page shows it instead of
// claiming «подключено» for a node nobody reached (VPS key change 09.11).
type Probe struct {
	OK    bool      `json:"ok"`
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// Inspection is a bounded, read-only capability probe of a pinned SSH node.
// A key, TCP port or past login alone must never imply a worker is ready.
type Inspection struct {
	OK          bool      `json:"ok"`
	At          time.Time `json:"at"`
	OS          string    `json:"os,omitempty"`
	Arch        string    `json:"arch,omitempty"`
	UID         uint64    `json:"uid,omitempty"`
	AvailableKB uint64    `json:"available_kb,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// HostKey is what a node presents now, compared with the pin.
type HostKey struct {
	Fingerprints []string `json:"fingerprints"`
	Pinned       []string `json:"pinned"`
	State        string   `json:"state"` // NEW, MATCH, CHANGED
}

func path(name, ext string) string { return filepath.Join(Dir, name+ext) }

func check(name string) error {
	if !validName.MatchString(name) {
		return errors.New("имя узла: строчные латинские буквы, цифры и дефис, до 40 символов")
	}
	return nil
}

// List reads every key in Dir as a node.
func List() ([]Node, error) {
	entries, err := os.ReadDir(Dir)
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.Contains(name, ".") || !validName.MatchString(name) {
			continue
		}
		if _, err := os.Stat(path(name, ".pub")); err != nil {
			continue
		}
		out = append(out, Get(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get describes one node; missing parts stay empty.
func Get(name string) Node {
	n := Node{Name: name}
	if raw, err := os.ReadFile(path(name, ".pub")); err == nil {
		n.PublicKey = authorizedLine(strings.TrimSpace(string(raw)))
	}
	n.Pinned, _ = fingerprintsOfFile(path(name, ".known_hosts"))
	if a, err := address(name); err == nil {
		n.Addr = &a
	} else {
		n.AddressNote = err.Error()
	}
	if raw, err := os.ReadFile(path(name, ".check")); err == nil {
		var p Probe
		if json.Unmarshal(raw, &p) == nil {
			n.LastCheck = &p
		}
	}
	if raw, err := os.ReadFile(path(name, ".inspect")); err == nil {
		var p Inspection
		if json.Unmarshal(raw, &p) == nil {
			n.LastInspect = &p
		}
	}
	return n
}

// authorizedLine restricts the MCPshka key on the node: no pty, no agent or
// X11 forwarding; port forwarding is granted per channel by install-oob.
func authorizedLine(pub string) string {
	if pub == "" || strings.HasPrefix(pub, "restrict") {
		return pub
	}
	return "restrict " + pub
}

func address(name string) (Addr, error) {
	if raw, err := os.ReadFile(path(name, ".json")); err == nil {
		var a Addr
		if json.Unmarshal(raw, &a) != nil || validAddr(a) != nil {
			return Addr{}, errors.New("адрес узла повреждён")
		}
		return a, nil
	}
	var cfg struct {
		Paths []struct{ Target, Key string }
	}
	if raw, err := os.ReadFile(OOBConfig); err == nil && json.Unmarshal(raw, &cfg) == nil {
		for _, p := range cfg.Paths {
			if p.Key == path(name, "") {
				if user, host, ok := strings.Cut(p.Target, "@"); ok {
					return Addr{Host: host, Port: 22, User: user}, nil
				}
			}
		}
	}
	return Addr{}, errors.New("адрес не записан")
}

func validAddr(a Addr) error {
	if net.ParseIP(a.Host) == nil && !regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`).MatchString(a.Host) {
		return errors.New("адрес: IP или имя хоста")
	}
	if a.Port < 1 || a.Port > 65535 {
		return errors.New("порт 1..65535")
	}
	if !validUser.MatchString(a.User) {
		return errors.New("пользователь: латиница, цифры, _ и -")
	}
	return nil
}

func run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errors.New(msg)
	}
	return out.Bytes(), nil
}

// Add creates the node's key and address. The owner then adds PublicKey to
// ~/.ssh/authorized_keys of User on the node and accepts its host key.
func Add(ctx context.Context, name string, a Addr) (Node, error) {
	if err := check(name); err != nil {
		return Node{}, err
	}
	if err := validAddr(a); err != nil {
		return Node{}, err
	}
	if _, err := os.Stat(path(name, "")); err == nil {
		return Node{}, errors.New("узел с таким именем уже есть")
	}
	if _, err := run(ctx, nil, sshKeygen, "-q", "-t", "ed25519", "-N", "", "-C", "mcpshka-"+name, "-f", path(name, "")); err != nil {
		return Node{}, fmt.Errorf("ключ не создан: %w", err)
	}
	raw, _ := json.Marshal(a)
	if err := writeAtomic(path(name, ".json"), raw); err != nil {
		return Node{}, err
	}
	return Get(name), nil
}

// Scan asks the node for its host key now and compares it with the pin.
func Scan(ctx context.Context, name string) (HostKey, []byte, error) {
	if err := check(name); err != nil {
		return HostKey{}, nil, err
	}
	a, err := address(name)
	if err != nil {
		return HostKey{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lines, err := run(ctx, nil, sshKeyscan, "-T", "8", "-t", "ed25519,ecdsa,rsa", "-p", strconv.Itoa(a.Port), a.Host)
	if len(bytes.TrimSpace(lines)) == 0 {
		if err == nil {
			err = errors.New("узел не ответил ключом")
		}
		return HostKey{}, nil, fmt.Errorf("ключ хоста не получен: %w", err)
	}
	fps, err := fingerprints(ctx, lines)
	if err != nil {
		return HostKey{}, nil, err
	}
	pinned, _ := fingerprintsOfFile(path(name, ".known_hosts"))
	hk := HostKey{Fingerprints: fps, Pinned: pinned, State: "NEW"}
	if len(pinned) > 0 {
		hk.State = "CHANGED"
		for _, f := range fps {
			for _, p := range pinned {
				if f == p {
					hk.State = "MATCH"
				}
			}
		}
	}
	return hk, lines, nil
}

// Accept pins the host key the node presents, but only if it has exactly
// the fingerprint the owner confirmed: a key swapped between showing and
// accepting is refused.
func Accept(ctx context.Context, name, fingerprint string) (HostKey, error) {
	hk, lines, err := Scan(ctx, name)
	if err != nil {
		return hk, err
	}
	a, _ := address(name)
	var keep []string
	sc := bufio.NewScanner(bytes.NewReader(lines))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fps, err := fingerprints(ctx, []byte(line+"\n"))
		if err == nil && len(fps) == 1 && fps[0] == fingerprint {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				keep = append(keep, a.hostPattern()+" "+fields[1]+" "+fields[2])
			}
		}
	}
	if len(keep) == 0 {
		return hk, errors.New("узел сейчас показывает другой ключ — не принят; сверьте отпечаток ещё раз")
	}
	if err := writeAtomic(path(name, ".known_hosts"), []byte(strings.Join(keep, "\n")+"\n")); err != nil {
		return hk, err
	}
	hk.Pinned, hk.State = []string{fingerprint}, "MATCH"
	_ = os.Remove(path(name, ".check")) // new pin: the old verdict no longer applies
	return hk, nil
}

// Check logs in with the node key and the pinned host key only.
func Check(ctx context.Context, name string) error {
	if err := check(name); err != nil {
		return err
	}
	a, err := address(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = run(ctx, nil, sshClient, "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+path(name, ".known_hosts"), "-o", "IdentitiesOnly=yes",
		"-i", path(name, ""), "-p", strconv.Itoa(a.Port), a.User+"@"+a.Host, "true")
	p := Probe{OK: err == nil, At: time.Now().UTC()}
	if err != nil {
		p.Error = err.Error()
		if len(p.Error) > 300 {
			p.Error = p.Error[len(p.Error)-300:]
		}
	}
	if raw, merr := json.Marshal(p); merr == nil {
		_ = writeAtomic(path(name, ".check"), raw)
	}
	return err
}

// Inspect logs in without root privileges and obtains a real OS/CPU/disk
// profile. No install, network mutation, password or privileged command is run.
// The pinned host key is mandatory; failures clear any prior success verdict.
func Inspect(ctx context.Context, name string) (Inspection, error) {
	p := Inspection{At: time.Now().UTC()}
	if err := check(name); err != nil {
		return p, err
	}
	a, err := address(name)
	if err != nil {
		return p, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// All remote commands are fixed literals, not constructed from user input.
	// OpenSSH checks the key before sending any authentication credentials.
	out, err := run(ctx, nil, sshClient,
		"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+path(name, ".known_hosts"),
		"-o", "IdentitiesOnly=yes", "-o", "ClearAllForwardings=yes", "-o", "RequestTTY=no",
		"-T", "-i", path(name, ""), "-p", strconv.Itoa(a.Port), a.User+"@"+a.Host,
		"uname -s; uname -m; id -u; df -Pk / | tail -n 1")
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != 4 {
			err = errors.New("unexpected remote capability probe output")
		} else {
			p.OS, p.Arch = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
			p.UID, err = strconv.ParseUint(strings.TrimSpace(lines[2]), 10, 32)
			if err == nil && (p.OS != "Linux" || p.Arch == "" || p.UID == 0) {
				err = errors.New("node must be Linux with a non-root SSH account")
			}
			if err == nil {
				fields := strings.Fields(lines[3])
				if len(fields) < 4 {
					err = errors.New("remote disk capacity probe incomplete")
				} else {
					p.AvailableKB, err = strconv.ParseUint(fields[3], 10, 64)
				}
			}
		}
	}
	p.OK = err == nil
	if err != nil {
		p.Error = err.Error()
		if len(p.Error) > 300 {
			p.Error = p.Error[len(p.Error)-300:]
		}
		p.OS, p.Arch, p.UID, p.AvailableKB = "", "", 0, 0
	}
	raw, merr := json.Marshal(p)
	if merr == nil {
		if werr := writeAtomic(path(name, ".inspect"), raw); werr != nil && err == nil {
			err = werr
		}
	}
	return p, err
}

// Remove moves the node's files aside (never deletes a key outright).
func Remove(name string) error {
	if err := check(name); err != nil {
		return err
	}
	if _, err := os.Stat(path(name, "")); err != nil {
		return errors.New("узла нет")
	}
	aside := filepath.Join(Dir, ".removed", name+"-"+time.Now().UTC().Format("20060102T150405"))
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return err
	}
	for _, ext := range []string{"", ".pub", ".known_hosts", ".json", ".check", ".inspect"} {
		if err := os.Rename(path(name, ext), filepath.Join(aside, name+ext)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func fingerprints(ctx context.Context, keyLines []byte) ([]string, error) {
	out, err := run(ctx, keyLines, sshKeygen, "-l", "-E", "sha256", "-f", "-")
	if err != nil {
		return nil, fmt.Errorf("отпечаток не вычислен: %w", err)
	}
	var fps []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[1], "SHA256:") {
			fps = append(fps, f[1])
		}
	}
	if len(fps) == 0 {
		return nil, errors.New("отпечаток не вычислен")
	}
	return fps, nil
}

func fingerprintsOfFile(file string) ([]string, error) {
	raw, err := os.ReadFile(file)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return fingerprints(ctx, raw)
}

func writeAtomic(file string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".node-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
