package webos

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// KeyDir holds the service's Developer Mode key for the TV (prisoner jail
// only) and its known_hosts; reads need no Face ID.
var (
	KeyDir  = "/var/lib/mcpshka/lg-tv"
	sshBin  = "ssh"
	Go2RTC  = "http://127.0.0.1:1984"
	sshOpts = []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=6", "-o", "LogLevel=ERROR", "-o", "HostKeyAlgorithms=+ssh-rsa", "-o", "PubkeyAcceptedKeyTypes=+ssh-rsa", "-p", "9922"}
)

// HostKeyAlias pins the TV's host key to the TV, not to its DHCP address: a
// new IP after a router change failed with "Host key verification failed"
// although the key and the TV were the same (MCP-133).
const HostKeyAlias = "lg-tv"

// pinHostAlias copies the single pinned TV key (recorded under its old IP)
// to the alias once. Several keys are ambiguous and left for the owner.
func pinHostAlias(path string) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return
	}
	var keys []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == HostKeyAlias {
			return
		}
		keys = append(keys, f[1]+" "+f[2])
	}
	if len(keys) != 1 {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return
	}
	defer f.Close()
	prefix := ""
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		prefix = "\n"
	}
	_, _ = f.WriteString(prefix + HostKeyAlias + " " + keys[0] + "\n")
}

type Fact struct {
	ID    string `json:"id"`
	State string `json:"state"` // PASS, FAIL, UNKNOWN
	Value string `json:"value"`
}

type TestResult struct {
	TV    string `json:"tv"`
	App   string `json:"app"`
	Facts []Fact `json:"facts"`
	Pass  bool   `json:"pass"`
}

func tvSSH(ctx context.Context, ip string, tty bool, command string) (string, error) {
	args := append([]string{}, sshOpts...)
	if tty {
		args = append([]string{"-tt"}, args...)
	} else {
		args = append([]string{"-T"}, args...)
	}
	known := filepath.Join(KeyDir, "known_hosts")
	pinHostAlias(known)
	args = append(args, "-o", "UserKnownHostsFile="+known, "-o", "HostKeyAlias="+HostKeyAlias, "-i", filepath.Join(KeyDir, "prisoner_key"), "prisoner@"+ip, command)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, sshBin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// The TV's own words (jail, PTY, unknown command) make an UNKNOWN
		// fact actionable instead of a bare "unreachable".
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, tail(msg, 200))
		}
	}
	return string(out), err
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// runningApps asks applicationManager which apps run. Developer Mode's jail
// allows only luna-send-pub; it answers without a terminal on current
// firmware, older ones want one, so a terminal is the fallback.
func runningApps(ctx context.Context, ip string) (string, error) {
	const q = `/usr/bin/luna-send-pub -n 1 luna://com.webos.applicationManager/running '{}'`
	out, err := tvSSH(ctx, ip, false, q)
	if strings.Contains(out, "returnValue") {
		return out, nil
	}
	out2, err2 := tvSSH(ctx, ip, true, q)
	if strings.Contains(out2, "returnValue") {
		return out2, nil
	}
	// Report the non-terminal attempt first: "PTY allocation request
	// failed" only says the jail refuses a terminal, not why the plain
	// call gave no answer (27.10: running UNKNOWN while installed PASS).
	plain := "no answer"
	if err != nil {
		plain = err.Error()
	} else if o := strings.TrimSpace(out); o != "" {
		plain = fmt.Sprintf("no returnValue in %q", tail(o, 160))
	}
	if err2 != nil && strings.Contains(err2.Error(), "PTY allocation") {
		return "", fmt.Errorf("without terminal: %s; terminal fallback refused by the TV jail (PTY), so only the plain call counts", plain)
	}
	if err2 != nil {
		return "", fmt.Errorf("without terminal: %s; with terminal: %v", plain, err2)
	}
	return "", fmt.Errorf("without terminal: %s; with terminal: no answer %q", plain, tail(strings.TrimSpace(out2), 120))
}

// consumerRate samples go2rtc's bytes sent to the TV's consumers of stream.
func consumerRate(ctx context.Context, stream, ip string, gap time.Duration) (int64, bool, error) {
	read := func() (int64, bool, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, Go2RTC+"/api/streams?src="+url.QueryEscape(stream), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, false, err
		}
		defer resp.Body.Close()
		var v struct {
			Consumers []struct {
				RemoteAddr string `json:"remote_addr"`
				Send       int64  `json:"send"`
			} `json:"consumers"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return 0, false, err
		}
		var sent int64
		found := false
		for _, c := range v.Consumers {
			if host, _, err := net.SplitHostPort(c.RemoteAddr); err == nil && host == ip {
				sent, found = sent+c.Send, true
			}
		}
		return sent, found, nil
	}
	a, found, err := read()
	if err != nil || !found {
		return 0, found, err
	}
	time.Sleep(gap)
	b, _, err := read()
	return (b - a) * int64(time.Second) / int64(gap), true, err
}

// Test checks a webOS app and, when stream is set, the go2rtc stream it
// shows: reachable TV, installed version, running, bytes reaching the TV.
func Test(ctx context.Context, ip, app, stream string, gap time.Duration) TestResult {
	r := TestResult{TV: ip, App: app, Facts: []Fact{}}
	add := func(id, state, value string) { r.Facts = append(r.Facts, Fact{id, state, value}) }
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "9922"), 4*time.Second)
	if err != nil {
		add("developer_mode", "FAIL", "port 9922 closed: TV off or Developer Mode expired (turn it on via ha_service_call)")
		return r
	}
	conn.Close()
	add("developer_mode", "PASS", "port 9922 open")
	info, err := tvSSH(ctx, ip, false, "cat /media/developer/apps/usr/palm/applications/"+app+"/appinfo.json")
	var a AppInfo
	if err != nil {
		add("installed", "UNKNOWN", "SSH/appinfo: "+tail(err.Error(), 250))
	} else if json.Unmarshal([]byte(info), &a) != nil || a.Version == "" {
		add("installed", "FAIL", app+" not installed or appinfo invalid")
	} else {
		add("installed", "PASS", app+" "+a.Version)
	}
	running, err := runningApps(ctx, ip)
	switch {
	case err != nil:
		add("running", "UNKNOWN", "applicationManager: "+err.Error())
	case strings.Contains(running, `"`+app+`"`):
		add("running", "PASS", app+" is running")
	default:
		add("running", "FAIL", app+" is not running")
	}
	if stream != "" {
		rate, found, err := consumerRate(ctx, stream, ip, gap)
		switch {
		case err != nil:
			add("stream", "UNKNOWN", err.Error())
		case !found:
			add("stream", "FAIL", "go2rtc has no consumer of "+stream+" from the TV")
		case rate <= 0:
			add("stream", "FAIL", "TV consumer of "+stream+" receives nothing")
		default:
			add("stream", "PASS", fmt.Sprintf("TV receives %s at %d kbit/s", stream, rate*8/1000))
		}
	}
	r.Pass = true
	for _, f := range r.Facts {
		r.Pass = r.Pass && f.State == "PASS"
	}
	return r
}
