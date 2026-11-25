// Package webos deploys any IPK of a project to an LG TV in Developer Mode and
// confirms the installed version from the TV itself. The TV key belongs to
// root, so the install runs as one approved_task: the owner's Face ID covers
// exactly this interpreter, inline code and pinned script/appinfo/IPK.
package webos

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mcpshka/internal/rootbroker"
)

var ipRe = regexp.MustCompile(`^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$`)

// marker precedes the TV's appinfo.json in the task output.
const marker = "TV_APPINFO "

type AppInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// IPKInfo reads Package and Version from the IPK's own control file
// (an ar archive holding control.tar.gz), so any app — not one fixed
// project — can be deployed and checked.
func IPKInfo(path string) (AppInfo, error) {
	var a AppInfo
	f, err := os.Open(path)
	if err != nil {
		return a, err
	}
	defer f.Close()
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "!<arch>\n" {
		return a, errors.New("not an IPK (ar) archive")
	}
	for {
		hdr := make([]byte, 60)
		if _, err := io.ReadFull(f, hdr); err != nil {
			return a, errors.New("IPK has no control.tar.gz")
		}
		name := strings.TrimRight(strings.TrimSpace(string(hdr[:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size < 0 || size > 64<<20 {
			return a, errors.New("corrupt IPK header")
		}
		if name != "control.tar.gz" {
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return a, err
			}
			continue
		}
		gz, err := gzip.NewReader(io.LimitReader(f, size))
		if err != nil {
			return a, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				return a, errors.New("control.tar.gz has no control file")
			}
			if strings.TrimPrefix(h.Name, "./") != "control" {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(tr, 64<<10))
			for _, line := range strings.Split(string(body), "\n") {
				if v, ok := strings.CutPrefix(line, "Package:"); ok {
					a.ID = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "Version:"); ok {
					a.Version = strings.TrimSpace(v)
				}
			}
			if a.ID == "" || a.Version == "" {
				return a, errors.New("IPK control lacks Package or Version")
			}
			return a, nil
		}
	}
}

// Plan pins the project's own installer (webos/install.py) and the repo's IPK.
func Plan(repo, ip, ipk string) (rootbroker.ApprovedPlan, AppInfo, error) {
	return PlanFrom(repo, ip, ipk, "", false)
}

// PlanFrom pins the project's own installer and the IPK file pkg (default
// repo/ipk; a sealed pipeline export otherwise), and returns the app id and
// version the TV must report afterwards. The installer learns the pinned file
// and an owner-approved downgrade from MCPSHKA_WEBOS_IPK and
// MCPSHKA_WEBOS_ALLOW_DOWNGRADE (=the target version), besides the legacy
// module globals IPK/APP_ID.
func PlanFrom(repo, ip, ipk, pkg string, allowDowngrade bool) (rootbroker.ApprovedPlan, AppInfo, error) {
	clean := filepath.Clean(ipk)
	if !ipRe.MatchString(ip) || !filepath.IsAbs(repo) || ipk == "" || filepath.IsAbs(ipk) || clean != ipk || strings.HasPrefix(clean, "..") || !strings.HasSuffix(ipk, ".ipk") {
		return rootbroker.ApprovedPlan{}, AppInfo{}, errors.New("need an absolute repo, the TV's IPv4 and a repo-relative .ipk")
	}
	webosDir := filepath.Join(repo, "webos")
	if pkg == "" {
		pkg = filepath.Join(repo, ipk)
	}
	if !filepath.IsAbs(pkg) || filepath.Base(pkg) != filepath.Base(ipk) {
		return rootbroker.ApprovedPlan{}, AppInfo{}, errors.New("package file must be the absolute path of the named .ipk")
	}
	want, err := IPKInfo(pkg)
	if err != nil {
		return rootbroker.ApprovedPlan{}, want, err
	}
	py, err := filepath.EvalSymlinks("/usr/bin/python3")
	if err != nil {
		return rootbroker.ApprovedPlan{}, want, err
	}
	// One line (approved_task args carry no newlines). Register the readback
	// before running the installer, so a failed provision step still reports
	// an installed version without disguising the installer's exit code.
	down := ""
	if allowDowngrade {
		down = want.Version
	}
	code := fmt.Sprintf(`import atexit,os,sys;D,IP,P,A,V=sys.argv[1:];os.environ['MCPSHKA_WEBOS_IPK']=P;os.environ['MCPSHKA_WEBOS_ALLOW_DOWNGRADE']=V;sys.path.insert(0,D);sys.argv=['install.py',IP];import install as m;m.IPK=P;m.APP_ID=A;atexit.register(lambda:(lambda r:print(%q+' '.join(r.stdout.split())) if r.stdout else None)(m.ssh(IP,m.key_path(IP),'cat /media/developer/apps/usr/palm/applications/'+A+'/appinfo.json'))) if hasattr(m,'key_path') else None;sys.exit(m.main())`, marker)
	plan := rootbroker.ApprovedPlan{
		AllowDowngrade: allowDowngrade,
		TargetVersion:  down,
		Executable:     py,
		Args:           []string{"-I", "-c", code, webosDir, ip, pkg, want.ID, down},
		Workdir:        repo,
		Network:        true,
		TimeoutSec:     300,
		Inputs:         []string{filepath.Join(webosDir, "install.py"), pkg},
	}
	// Developer Mode must answer before the owner's approval is spent
	// (27.10: Face ID passed, the TV was off, the task FAILED).
	if t := DevModeEndpoint(ip); rootbroker.ValidateEndpoint(t) == nil {
		plan.Reach = []string{t}
	}
	return plan, want, nil
}

// DevModeEndpoint is the TV's Developer Mode SSH port.
func DevModeEndpoint(ip string) string { return net.JoinHostPort(ip, "9922") }

// Confirm reads the TV's appinfo from the task output: the legacy readback
// marker, or an installer's own receipt line ({"app_id","version","state":
// "VERIFIED_ON_TV"|"INSTALLED_READBACK"}) read back from the TV.
func Confirm(output string, want AppInfo) (AppInfo, bool) {
	var got AppInfo
	i := strings.LastIndex(output, marker)
	if i < 0 {
		for _, line := range strings.Split(output, "\n") {
			var r struct {
				AppID   string `json:"app_id"`
				Version string `json:"version"`
				State   string `json:"state"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &r) == nil && (r.State == "VERIFIED_ON_TV" || r.State == "INSTALLED_READBACK" || r.State == "VERIFIED_ON_TV_RECOVERED") {
				got = AppInfo{ID: r.AppID, Version: r.Version}
			}
		}
		return got, got.ID != "" && got.ID == want.ID && got.Version == want.Version
	}
	line, _, _ := strings.Cut(output[i+len(marker):], "\n")
	if json.Unmarshal([]byte(line), &got) != nil {
		return got, false
	}
	return got, got.ID == want.ID && got.Version == want.Version
}
