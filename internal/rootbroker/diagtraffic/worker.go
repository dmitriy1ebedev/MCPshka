package diagtraffic

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func (d runDir) collector(argv []string, stdout, stderr string, limit int) (*process, error) {
	files := map[string]*os.File{}
	for _, name := range []string{stdout, stderr} {
		if name == "" || files[name] != nil {
			continue
		}
		f, err := d.create(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		files[name] = f
	}
	var out, problem *os.File = files[stdout], files[stderr]
	return launch(argv, nil, out, problem, limit)
}

func (d runDir) observe(ctx context.Context, t target, s state) (err error) {
	problems := []string{}
	observers := map[string]*process{}
	s["state"], s["started_at"], s["pid"] = "RUNNING", now(), os.Getpid()
	s["client"], s["mac"], s["duration_seconds"], s["passive_only"] = t.client, t.mac, t.duration, true
	if err := d.save(s); err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		_, _, cleanup := stopCollectors(observers)
		s["state"], s["finished_at"], s["cleanup"] = "FAILED", now(), cleanup
		s["errors"] = append(problems, "worker:"+err.Error())
		err = errors.Join(err, d.save(s))
	}()
	mac, err := readARP(t.client)
	switch {
	case err != nil:
		return fmt.Errorf("arp_read_failed: %w", err)
	case mac == "":
		return fmt.Errorf("target_identity_unknown: no complete ARP entry for %s", t.client)
	case mac != t.mac:
		return fmt.Errorf("target_identity_mismatch: client=%s expected=%s observed=%s", t.client, t.mac, mac)
	}

	client := netip.MustParseAddr(t.client)
	ipv6, discoveryError := discoverIPv6(ctx, t.mac)
	s["target_identity"] = map[string]string{"client": t.client, "expected_mac": t.mac, "observed_mac": mac, "state": "MATCH"}
	s["client_ipv6"], s["ipv6_discovery_error"] = stringsOfIPs(ipv6), discoveryError
	s["capture_filter"] = "( host " + t.client + " or ip6 )"
	if err := d.save(s); err != nil {
		return err
	}
	d.phase(ctx, "pre", client, ipv6, &problems)
	ended := "cancelled"
	if ctx.Err() == nil && !d.stopRequested() {
		clients := append([]netip.Addr{client}, ipv6...)
		if err := d.startCollectors(t, clients, observers, &problems); err != nil {
			return err
		}
		pids := map[string]int{}
		for name, p := range observers {
			pids[name] = p.cmd.Process.Pid
		}
		s["observer_pids"] = pids
		if err := d.save(s); err != nil {
			return err
		}
		ended = d.capture(ctx, t, clients, observers["tcpdump"], &problems)
	}
	tcpCode, ctCodes, cleanup := stopCollectors(observers)
	postIPv6, discoveryError := discoverIPv6(ctx, t.mac)
	ipv6 = uniqueIPv6(append(ipv6, postIPv6...))
	d.phase(ctx, "post", client, ipv6, &problems)
	pcaps := d.pcaps()
	for i, pcap := range pcaps {
		f, err := d.fs.OpenFile(pcap.Name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if err := checkedFile(f, false); err != nil {
			f.Close()
			problems = append(problems, err.Error())
			continue
		}
		if problem := d.render(ctx, i, f); problem != "" {
			problems = append(problems, problem)
		}
		f.Close()
	}
	if err := d.writeReport(d.summarize(client, ipv6, pcaps)); err != nil {
		return err
	}
	final := "SUCCESS"
	if len(pcaps) == 0 {
		problems = append(problems, "no_pcap_files")
		final = "FAILED"
	}
	if ended == "cancelled" {
		final = "CANCELLED"
	} else if ended == "mac_mismatch" {
		final = "FAILED"
	}
	s["state"], s["ended_by"], s["finished_at"] = final, ended, now()
	s["client_ipv6"], s["post_ipv6_discovery_error"] = stringsOfIPs(ipv6), discoveryError
	s["pcap_bytes"], s["pcap_byte_cap"], s["cleanup"] = pcapBytes(pcaps), pcapCap, cleanup
	s["tcpdump_rc"], s["conntrack_rc"], s["errors"] = tcpCode, ctCodes, problems
	s["report"], s["summary"] = "report.json", "summary.txt"
	return d.save(s)
}

func (d runDir) startCollectors(t target, clients []netip.Addr, observers map[string]*process, problems *[]string) error {
	argv := []string{"/usr/bin/tcpdump", "-i", "any", "-nn", "-U", "-s", "1024", "-B", "4096", "-Z", "root",
		"-w", "-", "(", "host", t.client, "or", "ip6", ")"}
	p, err := d.collector(argv, "traffic.pcap", "tcpdump.stderr.log", pcapCap)
	if err != nil {
		return fmt.Errorf("tcpdump_start: %w", err)
	}
	observers["tcpdump"] = p
	for i, addr := range clients {
		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("6_%d", i-1)
		}
		name := "conntrack" + suffix
		p, err := d.collector(conntrack("-E", addr, "-s", "timestamp,extended"),
			"conntrack.events"+suffix+".log", "conntrack.stderr"+suffix+".log", textLimit)
		if err != nil {
			*problems = append(*problems, name+"_start:"+err.Error())
			continue
		}
		observers[name] = p
	}
	return nil
}

func (d runDir) capture(ctx context.Context, t target, clients []netip.Addr, tcp *process, problems *[]string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.duration)*time.Second)
	defer cancel()
	samples, err := d.create("conntrack.samples.log")
	if err != nil {
		*problems = append(*problems, "conntrack.samples:"+err.Error())
	} else {
		defer samples.Close()
	}
	remaining, nextMAC := int64(textLimit), time.Now().Add(10*time.Second)
	for ctx.Err() == nil {
		if d.stopRequested() {
			return "cancelled"
		}
		if tcp.stopped() {
			if pcapBytes(d.pcaps()) >= pcapCap-65536 {
				return "byte_cap"
			}
			return "tcpdump_exited"
		}
		if time.Now().After(nextMAC) {
			nextMAC = time.Now().Add(10 * time.Second)
			if mac, _ := readARP(t.client); mac != "" && mac != t.mac {
				*problems = append(*problems, "target_identity_mismatch_during_capture: observed="+mac)
				return "mac_mismatch"
			}
		}
		if samples != nil {
			remaining -= appendSamples(ctx, samples, clients, remaining)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-timer.C:
		case <-tcp.done:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	if ctx.Err() == context.Canceled || d.stopRequested() {
		return "cancelled"
	}
	return "duration"
}

func stopCollectors(observers map[string]*process) (tcpCode any, conntrack map[string]int, cleanup map[string]any) {
	conntrack = map[string]int{}
	alive := map[string]bool{}
	allStopped := true
	for name, p := range observers {
		rc := p.stop(syscall.SIGINT, 2*time.Second)
		if name == "tcpdump" {
			tcpCode = rc
		} else {
			conntrack[name] = rc
		}
		alive[name] = !p.stopped()
		allStopped = allStopped && !alive[name]
	}
	return tcpCode, conntrack, map[string]any{"checked_at": now(), "observers_alive": alive, "all_stopped": allStopped}
}
