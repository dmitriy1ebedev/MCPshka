package webos

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeIPK writes an ar archive with control.tar.gz holding ./control.
func fakeIPK(t *testing.T, path, id, version string) {
	t.Helper()
	var ctl bytes.Buffer
	gz := gzip.NewWriter(&ctl)
	tw := tar.NewWriter(gz)
	body := fmt.Sprintf("Package: %s\nVersion: %s\n", id, version)
	tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(body))})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", ctl.Bytes()}} {
		fmt.Fprintf(&ar, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", m.name, "0", "0", "0", "100644", len(m.data))
		ar.Write(m.data)
		if len(m.data)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	os.WriteFile(path, ar.Bytes(), 0o644)
}

func TestPlanFromAnyIPKAndConfirm(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "webos"), 0o755)
	fakeIPK(t, filepath.Join(repo, "go2rtc-webos-pip.ipk"), "com.go2rtc.pip", "0.2.1")
	p, want, err := Plan(repo, "192.168.77.40", "go2rtc-webos-pip.ipk")
	if err != nil {
		t.Skip(err) // host without /usr/bin/python3
	}
	if want.ID != "com.go2rtc.pip" || want.Version != "0.2.1" || len(p.Inputs) != 2 || p.Args[6] != "com.go2rtc.pip" {
		t.Fatalf("%+v %+v", p, want)
	}
	if strings.ContainsAny(p.Args[2], "\n") {
		t.Fatal("inline code must be one line")
	}
	if len(p.Reach) != 1 || p.Reach[0] != "192.168.77.40:9922" {
		t.Fatalf("Developer Mode must be a reach preflight before the approval is spent: %v", p.Reach)
	}
	for _, bad := range [][2]string{{"192.168.0.999", "go2rtc-webos-pip.ipk"}, {"192.168.77.40", "../x.ipk"}, {"192.168.77.40", "x.tar"}} {
		if _, _, err := Plan(repo, bad[0], bad[1]); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	out := "Installing ...\nTV_APPINFO {\"id\":\"com.go2rtc.pip\",\"version\":\"0.2.1\"}\n"
	if got, ok := Confirm(out, want); !ok || got.Version != "0.2.1" {
		t.Fatalf("%+v %v", got, ok)
	}
	if _, ok := Confirm("TV_APPINFO {\"id\":\"com.go2rtc.pip\",\"version\":\"0.2.0\"}", want); ok {
		t.Fatal("old version confirmed")
	}
}

func TestMCP194ReadbackPreservesFailedProvisionExit(t *testing.T) {
	for _, main := range []string{"return 2", "raise SystemExit(2)"} {
		t.Run(main, func(t *testing.T) {
			repo := t.TempDir()
			dir := filepath.Join(repo, "webos")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			fakeIPK(t, filepath.Join(repo, "test.ipk"), "com.go2rtc.pip", "0.2.1")
			p, want, err := Plan(repo, "192.0.2.10", "test.ipk")
			if err != nil {
				t.Fatal(err)
			}
			installer := "from types import SimpleNamespace\n" +
				"def key_path(ip): return 'unused-test-key'\n" +
				"def ssh(ip, key, command): return SimpleNamespace(stdout='{\"id\":\"com.go2rtc.pip\",\"version\":\"0.2.1\"}')\n" +
				"def main(): " + main + "\n"
			if err := os.WriteFile(filepath.Join(dir, "install.py"), []byte(installer), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(t.Context(), p.Executable, p.Args...).CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Fatalf("installer exit changed: %v, %s", err, out)
			}
			if _, ok := Confirm(string(out), want); !ok {
				t.Fatalf("observed app version lost after provisioning failed: %s", out)
			}
		})
	}
}

func TestPlanFromPinnedExportDowngradeAndReceipt(t *testing.T) {
	repo, store := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(repo, "webos"), 0o755)
	pkg := filepath.Join(store, "out", "app-0.1.0.ipk")
	os.MkdirAll(filepath.Dir(pkg), 0o700)
	fakeIPK(t, pkg, "com.go2rtc.pip", "0.1.0")
	p, want, err := PlanFrom(repo, "192.168.77.40", "out/app-0.1.0.ipk", pkg, true)
	if err != nil {
		t.Skip(err) // host without /usr/bin/python3
	}
	if !p.AllowDowngrade || p.TargetVersion != "0.1.0" || !strings.Contains(strings.Join(p.Environment(), "\n"), "MCPSHKA_ALLOW_DOWNGRADE=0.1.0") || p.Inputs[1] != pkg || p.Args[7] != "0.1.0" || p.Args[5] != pkg {
		t.Fatalf("export and downgrade must be pinned in the approved plan: %+v", p)
	}
	if p, _, _ := PlanFrom(repo, "192.168.77.40", "out/app-0.1.0.ipk", pkg, false); p.Args[7] != "" {
		t.Fatal("downgrade allowed without the owner's flag")
	}
	if _, _, err := PlanFrom(repo, "192.168.77.40", "out/other.ipk", pkg, false); err == nil {
		t.Fatal("file of another name accepted")
	}
	for _, arg := range p.Args {
		if len(arg) > 512 {
			t.Fatalf("approved_task argument exceeds 512 bytes: %d", len(arg))
		}
	}
	installer := "import os,json\ndef main():\n assert os.environ['MCPSHKA_WEBOS_IPK'] == IPK\n assert os.environ['MCPSHKA_WEBOS_ALLOW_DOWNGRADE'] == os.environ['MCPSHKA_ALLOW_DOWNGRADE'] == '0.1.0'\n print(json.dumps({'app_id': APP_ID, 'version': '0.1.0', 'state': 'VERIFIED_ON_TV'}))\n return 0\n"
	if err := os.WriteFile(filepath.Join(repo, "webos", "install.py"), []byte(installer), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(p.Executable, p.Args...)
	cmd.Env = p.Environment()
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer stub: %v: %s", err, raw)
	}
	if _, ok := Confirm(string(raw), want); !ok {
		t.Fatalf("installer receipt not confirmed: %s", raw)
	}
	receipt := `{"app_id": "com.go2rtc.pip", "state": "VERIFIED_ON_TV", "version": "0.1.0"}` + "\n" + `{"state": "LAUNCH_ACKNOWLEDGED_NOT_VIDEO_VERIFIED"}`
	if got, ok := Confirm(receipt, want); !ok || got.Version != "0.1.0" {
		t.Fatalf("installer receipt not confirmed: %+v", got)
	}
	if _, ok := Confirm(strings.Replace(receipt, "VERIFIED_ON_TV", "VERIFIED_ON_TV_RECOVERED", 1), want); !ok {
		t.Fatal("recovered device receipt not recognized")
	}
	if _, ok := Confirm(`{"app_id": "com.go2rtc.pip", "state": "REFUSED", "version": "0.1.0"}`, want); ok {
		t.Fatal("refused receipt confirmed")
	}
}
