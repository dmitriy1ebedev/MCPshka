// Package releaseprep stages fixed, digest-pinned artifacts without evaluating code.
package releaseprep

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const Command = "/usr/local/bin/mcpshka"
const maxArchive = 64 << 20 // = executor.MaxReleaseImage (BUG-20261023-80)

var versionPattern = regexp.MustCompile(`^0\.[0-9]{1,3}\.[0-9]{1,6}$`)
var inboxPattern = regexp.MustCompile(`^/var/lib/mcpshka/artifact-inbox/[a-f0-9]{32}\.blob$`)
var errInvalid = errors.New("release artifact validation failed; reconcile original job and candidate before retry")

type request struct {
	Version        string  `json:"version"`
	CurrentVersion string  `json:"current_version"`
	SourceDigest   string  `json:"source_digest"`
	Receipt        receipt `json:"receipt"`
}
type receipt struct {
	Schema       string            `json:"schema"`
	Provider     string            `json:"provider"`
	Repository   string            `json:"repository"`
	RunID        int64             `json:"run_id"`
	SourceSHA    string            `json:"source_sha"`
	HeadSHA      string            `json:"head_sha"`
	Tree         string            `json:"tree"`
	SourceDigest string            `json:"source_digest"`
	Conclusion   string            `json:"conclusion"`
	Suites       map[string]string `json:"suites"`
	ArtifactID   int64             `json:"artifact_id"`
	ArchiveSHA   string            `json:"artifact_archive_sha256"`
	BinarySHA    string            `json:"binary_sha256,omitempty"`
	BrokerSHA    string            `json:"broker_binary_sha256,omitempty"`
}
type identity struct {
	Schema       string `json:"schema"`
	SourceDigest string `json:"source_digest"`
	FileCount    int64  `json:"file_count"`
	TotalBytes   int64  `json:"total_bytes"`
}
type environment struct {
	repo, controller, broker, guarded, gate string
	owner                                   uint32
}

func production() environment {
	return environment{"/root/repos/mcpshka", Command, "/usr/local/libexec/mcpshka-broker", "/usr/local/libexec/mcpshka-self-deploy-guarded", "/usr/local/libexec/mcpshka-self-deploy", 0}
}
func sum(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func digest(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(s) == n && hex.EncodeToString(b) == s
}

// Strict JSON rejects duplicate fields at every object before typed decoding.
func exactFields(raw []byte, required, optional string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, errInvalid
	}
	allowed := map[string]bool{}
	for _, name := range strings.Fields(required) {
		allowed[name] = true
		if _, ok := fields[name]; !ok {
			return nil, errInvalid
		}
	}
	for _, name := range strings.Fields(optional) {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return nil, errInvalid
		}
	}
	return fields, nil
}
func strict(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 4 {
			return errInvalid
		}
		token, e := d.Token()
		if e != nil {
			return errInvalid
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' {
				return errInvalid
			}
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				name, ok := k.(string)
				if e != nil || !ok || seen[name] {
					return errInvalid
				}
				seen[name] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return errInvalid
			}
		}
		return nil
	}
	if value(0) != nil {
		return errInvalid
	}
	if _, e := d.Token(); e != io.EOF {
		return errInvalid
	}
	switch out.(type) {
	case *request:
		fields, e := exactFields(raw, "version current_version source_digest receipt", "")
		if e != nil {
			return e
		}
		ci, e := exactFields(fields["receipt"], "schema provider repository run_id source_sha head_sha tree source_digest conclusion suites artifact_id artifact_archive_sha256", "binary_sha256 broker_binary_sha256")
		if e != nil {
			return e
		}
		for _, name := range []string{"binary_sha256", "broker_binary_sha256"} {
			if v, ok := ci[name]; ok {
				var s string
				if json.Unmarshal(v, &s) != nil || !digest(s, 64) {
					return errInvalid
				}
			}
		}
	case *LocalRequest:
		if _, e := exactFields(raw, "source version current_version source_digest build_permit launch_id test_permit", ""); e != nil {
			return e
		}
	case *LocalReceipt:
		fields, e := exactFields(raw, "schema provider repository source_sha head_sha tree source_digest build_permit build_env_digest binary_sha256 broker_binary_sha256 test_evidence", "")
		if e != nil {
			return e
		}
		if _, e = exactFields(fields["test_evidence"], "kind id tree conclusion suites", ""); e != nil {
			return e
		}
	case *identity:
		if _, e := exactFields(raw, "schema source_digest file_count total_bytes", ""); e != nil {
			return e
		}
	default:
		return errInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errInvalid
	}
	return nil
}
func parse(raw []byte) (request, error) {
	var r request
	if len(raw) > 8192 || strict(raw, &r) != nil || !versionPattern.MatchString(r.Version) || !versionPattern.MatchString(r.CurrentVersion) || r.Version == r.CurrentVersion || !digest(r.SourceDigest, 64) {
		return r, errInvalid
	}
	c := r.Receipt
	if (c.Schema != "mcpshka.operator-verified-ci/v1" && c.Schema != "mcpshka.operator-verified-ci/v2") || c.Provider != "github" || c.Repository != "dmitriy1ebedev/mcpshka" || c.RunID <= 0 || c.ArtifactID <= 0 || c.Conclusion != "success" || c.SourceDigest != r.SourceDigest || !digest(c.SourceSHA, 40) || !digest(c.HeadSHA, 40) || !digest(c.Tree, 40) || !digest(c.ArchiveSHA, 64) || len(c.Suites) != 5 {
		return r, errInvalid
	}
	for _, s := range []string{"release", "race", "fuzz", "build", "release-arm64"} {
		if c.Suites[s] != "success" {
			return r, errInvalid
		}
	}
	if c.BinarySHA != "" && !digest(c.BinarySHA, 64) || c.BrokerSHA != "" && !digest(c.BrokerSHA, 64) || c.Schema == "mcpshka.operator-verified-ci/v1" && c.BrokerSHA != "" {
		return r, errInvalid
	}
	var number int
	fmt.Sscanf(r.Version, "0.1.%d", &number)
	if number >= 134 && c.Schema != "mcpshka.operator-verified-ci/v2" {
		return r, errInvalid
	}
	return r, nil
}
func validateURL(raw string) error {
	if len(raw) > 8192 {
		return errInvalid
	}
	for _, c := range raw {
		if c <= 32 {
			return errInvalid
		}
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".oaiusercontent.com") || u.User != nil || u.Fragment != "" || u.Port() != "" && u.Port() != "443" {
		return errInvalid
	}
	return nil
}
func download(ctx context.Context, raw string) ([]byte, error) {
	if validateURL(raw) != nil {
		return nil, errInvalid
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errInvalid }}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, errInvalid
	}
	response, e := client.Do(req)
	if e != nil {
		return nil, errors.New("release artifact download failed; reconcile original job before retry")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maxArchive {
		return nil, errInvalid
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, maxArchive+1))
	if e != nil || len(data) > maxArchive {
		return nil, errInvalid
	}
	return data, nil
}
func unpack(raw []byte, r request) ([]byte, []byte, identity, error) {
	var id identity
	if len(raw) < 22 || len(raw) > maxArchive || sum(raw) != r.Receipt.ArchiveSHA {
		return nil, nil, id, errInvalid
	}
	start := max(0, len(raw)-65557)
	end := bytes.LastIndex(raw[start:], []byte("PK\x05\x06"))
	if end < 0 {
		return nil, nil, id, errInvalid
	}
	end += start
	if end+22 > len(raw) {
		return nil, nil, id, errInvalid
	}
	tail := raw[end:]
	count := int(binary.LittleEndian.Uint16(tail[10:12]))
	size := uint64(binary.LittleEndian.Uint32(tail[12:16]))
	offset := uint64(binary.LittleEndian.Uint32(tail[16:20]))
	comment := int(binary.LittleEndian.Uint16(tail[20:22]))
	if binary.LittleEndian.Uint16(tail[4:6]) != 0 || binary.LittleEndian.Uint16(tail[6:8]) != 0 || int(binary.LittleEndian.Uint16(tail[8:10])) != count || count != 8 && count != 11 || size > 16384 || offset+size != uint64(end) || end+22+comment != len(raw) {
		return nil, nil, id, errInvalid
	}
	archive, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil || len(archive.File) != count {
		return nil, nil, id, errInvalid
	}
	names := map[string]bool{}
	for _, n := range []string{"checks.log", "mcpshka-linux-arm64", "release-arm64-buildinfo.txt", "release-arm64-sha256.txt", "source-identity.json", "source-sha.txt", "source-tree.txt", "toolchain.txt"} {
		names[n] = true
	}
	if count == 11 {
		for _, n := range []string{"mcpshka-broker-linux-arm64", "broker-arm64-sha256.txt", "broker-arm64-buildinfo.txt"} {
			names[n] = true
		}
	}
	values := map[string][]byte{}
	for _, f := range archive.File {
		limit := uint64(65536)
		if f.Name == "mcpshka-linux-arm64" || f.Name == "mcpshka-broker-linux-arm64" {
			limit = maxArchive
		}
		if !names[f.Name] || values[f.Name] != nil || !f.Mode().IsRegular() || f.Flags&1 != 0 || f.Method != zip.Store && f.Method != zip.Deflate || f.UncompressedSize64 == 0 || f.UncompressedSize64 > limit {
			return nil, nil, id, errInvalid
		}
		stream, e := f.Open()
		if e != nil {
			return nil, nil, id, errInvalid
		}
		data, e := io.ReadAll(io.LimitReader(stream, int64(limit)+1))
		stream.Close()
		if e != nil || uint64(len(data)) != f.UncompressedSize64 {
			return nil, nil, id, errInvalid
		}
		values[f.Name] = data
	}
	if strict(values["source-identity.json"], &id) != nil || id.Schema != "mcpshka.source-identity/v1" || id.SourceDigest != r.SourceDigest || id.FileCount <= 0 || id.FileCount > 100000000 || id.TotalBytes <= 0 || id.TotalBytes > 100000000 || strings.TrimSpace(string(values["source-sha.txt"])) != r.Receipt.SourceSHA || strings.TrimSpace(string(values["source-tree.txt"])) != r.Receipt.Tree {
		return nil, nil, id, errInvalid
	}
	image := values["mcpshka-linux-arm64"]
	broker := values["mcpshka-broker-linux-arm64"]
	check := func(data []byte, name, checksum, build string) bool {
		parts := strings.Fields(string(values[checksum]))
		info := string(values[build])
		return len(parts) == 2 && parts[0] == sum(data) && filepath.Base(parts[1]) == name && len(data) >= 64 && bytes.Equal(data[:6], []byte("\x7fELF\x02\x01")) && binary.LittleEndian.Uint16(data[18:20]) == 183 && strings.Contains(info, "GOARCH=arm64") && strings.Contains(info, "CGO_ENABLED=1")
	}
	if !check(image, "mcpshka-linux-arm64", "release-arm64-sha256.txt", "release-arm64-buildinfo.txt") || r.Receipt.BinarySHA != "" && r.Receipt.BinarySHA != sum(image) {
		return nil, nil, id, errInvalid
	}
	if (r.Receipt.Schema == "mcpshka.operator-verified-ci/v2") != (broker != nil) || broker != nil && (!check(broker, "mcpshka-broker-linux-arm64", "broker-arm64-sha256.txt", "broker-arm64-buildinfo.txt") || r.Receipt.BrokerSHA != "" && r.Receipt.BrokerSHA != sum(broker)) {
		return nil, nil, id, errInvalid
	}
	return image, broker, id, nil
}

func sameFile(a, b os.FileInfo) bool {
	x, ok := a.Sys().(*syscall.Stat_t)
	y, other := b.Sys().(*syscall.Stat_t)
	return ok && other && os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime() == b.ModTime() && x.Ctim == y.Ctim && x.Uid == y.Uid && x.Nlink == y.Nlink
}
func readRegular(path string, limit int64, owner uint32, private bool) ([]byte, error) {
	var raw []byte
	err := scanRegular(path, limit, owner, private, func(r io.Reader) error { var e error; raw, e = io.ReadAll(r); return e })
	return raw, err
}
func hashRegular(path string, limit int64, owner uint32) (string, error) {
	h := sha256.New()
	err := scanRegular(path, limit, owner, false, func(r io.Reader) error { _, e := io.CopyBuffer(h, r, make([]byte, 65536)); return e })
	return hex.EncodeToString(h.Sum(nil)), err
}
func scanRegular(path string, limit int64, owner uint32, private bool, consume func(io.Reader) error) error {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if e != nil {
		return errInvalid
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return errInvalid
	}
	s, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || s.Nlink != 1 || s.Uid != owner || before.Mode().Perm()&0022 != 0 || private && before.Mode().Perm()&0077 != 0 || before.Size() <= 0 || before.Size() > limit {
		return errInvalid
	}
	reader := &io.LimitedReader{R: f, N: limit + 1}
	if e := consume(reader); e != nil {
		return errInvalid
	}
	after, e := f.Stat()
	named, nerr := os.Lstat(path)
	if e != nil || nerr != nil || !sameFile(before, after) || !sameFile(after, named) || limit+1-reader.N != before.Size() {
		return errInvalid
	}
	return nil
}
func writeVerified(root *os.Root, name string, raw []byte, mode os.FileMode) error {
	if filepath.Base(name) != name {
		return errInvalid
	}
	f, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(e, os.ErrExist) {
		f, e = root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return errInvalid
		}
		defer f.Close()
		before, e := f.Stat()
		if e != nil {
			return errInvalid
		}
		s, ok := before.Sys().(*syscall.Stat_t)
		if !ok || !before.Mode().IsRegular() || s.Nlink != 1 || s.Uid != uint32(os.Geteuid()) || before.Mode().Perm()&0077 != 0 || before.Size() != int64(len(raw)) {
			return errInvalid
		}
		data, e := io.ReadAll(io.LimitReader(f, int64(len(raw))+1))
		after, aerr := f.Stat()
		named, nerr := root.Lstat(name)
		if e != nil || aerr != nil || nerr != nil || !sameFile(before, after) || !sameFile(after, named) || !bytes.Equal(data, raw) {
			return errInvalid
		}
		return f.Chmod(mode)
	}
	if e != nil {
		return errInvalid
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = f.Chmod(mode)
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return errInvalid
	}
	return nil
}
func canonical(v any) []byte { raw, _ := json.Marshal(v); return raw }

var artifactStatfs = syscall.Statfs

func prepare(raw []byte, r request, env environment) (map[string]any, error) {
	var disk syscall.Statfs_t
	if artifactStatfs(env.repo, &disk) != nil || uint64(disk.Bavail)*uint64(disk.Bsize) < max(uint64(3<<30), uint64(disk.Blocks)*uint64(disk.Bsize)/10)+(96<<20) {
		return nil, errors.New("release artifact disk reserve insufficient")
	}
	image, broker, id, e := unpack(raw, r)
	if e != nil {
		return nil, e
	}
	c := r.Receipt
	c.BinarySHA = sum(image)
	if broker != nil {
		c.BrokerSHA = sum(broker)
	}
	// Maps preserve the installed Python gate's sorted canonical JSON contract.
	var ci map[string]json.RawMessage
	if json.Unmarshal(canonical(c), &ci) != nil {
		return nil, errInvalid
	}
	return stage(image, broker, id, request{Version: r.Version, CurrentVersion: r.CurrentVersion, SourceDigest: r.SourceDigest, Receipt: c}, env, canonical(ci))
}
func stage(image, broker []byte, id identity, r request, env environment, receiptRaw []byte) (map[string]any, error) {
	var disk syscall.Statfs_t
	if artifactStatfs(env.repo, &disk) != nil || uint64(disk.Bavail)*uint64(disk.Bsize) < max(uint64(3<<30), uint64(disk.Blocks)*uint64(disk.Bsize)/10)+(96<<20) {
		return nil, errors.New("release artifact disk reserve insufficient")
	}
	c := r.Receipt
	hashes := map[string]string{}
	for name, path := range map[string]string{"current_sha256": env.controller, "helper_sha256": env.guarded, "current_broker_sha256": env.broker, "current_gate_sha256": env.gate} {
		if broker == nil && name == "current_broker_sha256" {
			continue
		}
		v, e := hashRegular(path, 128<<20, env.owner)
		if e != nil {
			return nil, e
		}
		hashes[name] = v
	}
	plan := map[string]any{"schema": "mcpshka.release-plan/v1", "version": r.Version, "current_version": r.CurrentVersion, "source_digest": r.SourceDigest, "source_tree": c.Tree, "ci_run": c.RunID, "candidate_sha256": c.BinarySHA, "current_sha256": hashes["current_sha256"], "helper_sha256": hashes["helper_sha256"], "ci_receipt_sha256": sum(receiptRaw)}
	if broker != nil {
		plan["schema"] = "mcpshka.release-plan/v2"
		plan["broker_sha256"] = c.BrokerSHA
		plan["current_broker_sha256"] = hashes["current_broker_sha256"]
	}
	planRaw := canonical(plan)
	binRoot := filepath.Join(env.repo, "bin")
	real, e := filepath.EvalSymlinks(binRoot)
	if e != nil || real != binRoot {
		return nil, errInvalid
	}
	staging, e := os.OpenRoot(binRoot)
	if e != nil {
		return nil, errInvalid
	}
	defer staging.Close()
	write := func(path string, data []byte, mode os.FileMode) error {
		return writeVerified(staging, filepath.Base(path), data, mode)
	}
	candidate := filepath.Join(env.repo, "bin", "mcpshka-"+r.Version+"."+c.BinarySHA[:12]+".candidate")
	if e = write(candidate, image, 0700); e != nil {
		return nil, e
	}
	if e = write(candidate+".ci-receipt.json", receiptRaw, 0600); e != nil {
		return nil, e
	}
	if e = write(candidate+".release-plan.json", planRaw, 0600); e != nil {
		return nil, e
	}
	if broker != nil {
		brokerPath := filepath.Join(env.repo, "bin", "mcpshka-broker-"+r.Version+"."+c.BrokerSHA[:12]+".candidate")
		if e = write(brokerPath, broker, 0700); e != nil {
			return nil, e
		}
		helpers := map[string]string{}
		for name, file := range map[string]string{"helper_sha256": "coordinated-upgrade.py", "gate_sha256": "release-gate.py", "guarded_helper_sha256": "self-deploy.sh"} {
			v, e := hashRegular(filepath.Join(env.repo, "packaging", "native", file), 65536, uint32(os.Geteuid()))
			if e != nil {
				return nil, e
			}
			helpers[name] = v
		}
		paired := map[string]any{"schema": "mcpshka.coordinated-upgrade/v1", "operation_id": sum([]byte(c.SourceSHA + c.ArchiveSHA))[:32], "version": r.Version, "current_version": r.CurrentVersion, "ci_run": c.RunID, "source_tree": c.Tree, "controller_sha256": c.BinarySHA, "broker_sha256": c.BrokerSHA, "current_controller_sha256": hashes["current_sha256"], "current_broker_sha256": hashes["current_broker_sha256"], "current_gate_sha256": hashes["current_gate_sha256"], "current_guarded_sha256": hashes["helper_sha256"], "controller_candidate": candidate, "broker_candidate": brokerPath}
		for k, v := range helpers {
			paired[k] = v
		}
		if e = write(filepath.Join(env.repo, "bin", "mcpshka-coordinated-release-"+r.Version+".json"), canonical(paired), 0600); e != nil {
			return nil, e
		}
	}
	directory, e := staging.Open(".")
	if e != nil {
		return nil, errInvalid
	}
	e = directory.Sync()
	directory.Close()
	if e != nil {
		return nil, errInvalid
	}
	return map[string]any{"schema": "mcpshka.release-prepared/v1", "candidate": candidate, "plan_sha256": sum(planRaw), "binary_sha256": c.BinarySHA, "version": r.Version, "source_identity": id, "trust": "UNTRUSTED", "deploy_authorized": false}, nil
}

// Run accepts metadata in argv and the short-lived source only from private job environment.
func Run(ctx context.Context, raw []byte) (map[string]any, error) {
	var source struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(raw, &source) == nil && source.Source == "local" {
		return RunLocal(ctx, raw)
	}
	r, e := parse(raw)
	if e != nil {
		return nil, e
	}
	sourceURL, file := os.Getenv("MCPSHKA_RELEASE_URL"), os.Getenv("MCPSHKA_RELEASE_FILE")
	if (sourceURL == "") == (file == "") {
		return nil, errInvalid
	}
	var data []byte
	if file != "" {
		if !inboxPattern.MatchString(file) {
			return nil, errInvalid
		}
		data, e = readRegular(file, maxArchive, uint32(os.Geteuid()), true)
	} else {
		data, e = download(ctx, sourceURL)
	}
	if e != nil {
		return nil, e
	}
	return prepare(data, r, production())
}
