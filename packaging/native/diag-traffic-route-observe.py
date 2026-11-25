#!/usr/bin/env python3
"""Bounded, passive route-observation run for one LAN client.

Designed for short incident/lab captures on a Linux gateway. The worker never
changes routes, firewall rules, DNS, DHCP, services, or sysctls. It only reads
state and starts bounded tcpdump/conntrack observers.

Long work is detached: `start` returns a run id immediately; inspect with
`status` and `report`.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import resource
import shutil
import signal
import subprocess
import sys
import time

SCHEMA = "diag.traffic-route/v1alpha1"
DEFAULT_DURATION = 300
MIN_DURATION = 10
MAX_DURATION = 300
# One pcap file under a kernel RLIMIT_FSIZE: the capture can never exceed the
# cap or overwrite earlier packets (no -C/-W ring); tcpdump stops at the cap.
PCAP_BYTE_CAP = 32 * 1024 * 1024
MAC_RECHECK_SECONDS = 10
STOP_WAIT_SECONDS = 20
SNAPLEN = 1024
SNAPSHOT_LIMIT = 2 * 1024 * 1024
TEXT_LIMIT = 4 * 1024 * 1024


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def repo_root() -> Path:
    return Path(__file__).resolve().parents[1]


def default_runs_root() -> Path:
    return repo_root() / ".traffic-runs"


def atomic_json(path: Path, data: dict) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.chmod(tmp, 0o640)
    os.replace(tmp, path)


def safe_run_id() -> str:
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    entropy = hashlib.sha256(f"{os.getpid()}:{time.time_ns()}".encode()).hexdigest()[:8]
    return f"{stamp}-{entropy}"


def valid_client(value: str) -> str:
    ip = ipaddress.ip_address(value)
    if ip.version != 4:
        raise argparse.ArgumentTypeError("MVP capture currently requires an IPv4 client")
    if ip.is_loopback or ip.is_multicast or ip.is_unspecified or not ip.is_private:
        raise argparse.ArgumentTypeError("client must be a private unicast LAN IPv4 address")
    return str(ip)


def valid_mac(value: str) -> str:
    value = value.lower()
    if not re.fullmatch(r"(?:[0-9a-f]{2}:){5}[0-9a-f]{2}", value):
        raise argparse.ArgumentTypeError("MAC must look like aa:bb:cc:dd:ee:ff")
    return value


def verify_arp_identity(client: str, expected_mac: str | None) -> dict:
    result = {"client": client, "expected_mac": expected_mac or "", "observed_mac": "", "state": "UNVERIFIED"}
    if not expected_mac:
        return result
    try:
        lines = Path("/proc/net/arp").read_text(encoding="utf-8", errors="replace").splitlines()[1:]
    except OSError as exc:
        raise RuntimeError(f"arp_read_failed:{exc}") from exc
    for line in lines:
        fields = line.split()
        if len(fields) < 6 or fields[0] != client:
            continue
        flags = fields[2].lower()
        mac = fields[3].lower()
        if flags != "0x2" or mac == "00:00:00:00:00:00":
            continue
        result["observed_mac"] = mac
        if mac != expected_mac:
            result["state"] = "MISMATCH"
            raise RuntimeError(
                f"target_identity_mismatch: client={client} expected={expected_mac} observed={mac}"
            )
        result["state"] = "MATCH"
        return result
    raise RuntimeError(f"target_identity_unknown: no complete ARP entry for {client}")


def ensure_run_root(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    try:
        os.chmod(root, 0o2770)
    except PermissionError:
        pass


def state_path(run_dir: Path) -> Path:
    return run_dir / "state.json"


def write_state(run_dir: Path, **updates: object) -> dict:
    current: dict = {}
    path = state_path(run_dir)
    if path.exists():
        try:
            current = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            current = {}
    current.update(updates)
    current.setdefault("schemaVersion", SCHEMA)
    current.setdefault("run_id", run_dir.name)
    current["updated_at"] = utc_now()
    atomic_json(path, current)
    return current


def file_limit(limit: int):
    def apply() -> None:
        resource.setrlimit(resource.RLIMIT_FSIZE, (limit, limit))
    return apply


def snapshot(
    run_dir: Path,
    name: str,
    argv: list[str],
    *,
    timeout: float = 3.0,
    limit: int = SNAPSHOT_LIMIT,
) -> dict:
    out = run_dir / f"{name}.txt"
    result = {"name": name, "argv": argv, "path": out.name, "rc": None, "error": ""}
    try:
        with out.open("wb") as fh:
            proc = subprocess.Popen(
                argv,
                stdin=subprocess.DEVNULL,
                stdout=fh,
                stderr=subprocess.STDOUT,
                start_new_session=True,
                preexec_fn=file_limit(limit),
            )
            try:
                rc = proc.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    rc = proc.wait(timeout=0.5)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    rc = proc.wait(timeout=0.5)
                result["error"] = f"timeout>{timeout}s"
            result["rc"] = rc
    except FileNotFoundError:
        result["error"] = "command_not_found"
    except PermissionError as exc:
        result["error"] = f"permission:{exc}"
    except OSError as exc:
        result["error"] = f"os:{exc}"
    try:
        if out.exists():
            os.chmod(out, 0o640)
    except OSError:
        pass
    return result


def conntrack_argv(
    operation: str,
    address: str,
    direction: str = "-s",
    output: str = "extended",
) -> list[str]:
    ip = ipaddress.ip_address(address)
    argv = ["/usr/sbin/conntrack", operation]
    if ip.version == 6:
        argv.extend(["-f", "ipv6"])
    argv.extend([direction, str(ip), "-o", output])
    return argv


def baseline_commands(client: str, client_ipv6: list[str] | None = None) -> list[tuple[str, list[str]]]:
    commands: list[tuple[str, list[str]]] = [
        ("links", ["/usr/sbin/ip", "-o", "link", "show"]),
        ("addr4", ["/usr/sbin/ip", "-o", "-4", "addr", "show"]),
        ("addr6", ["/usr/sbin/ip", "-o", "-6", "addr", "show"]),
        ("neigh4", ["/usr/sbin/ip", "-4", "neigh", "show"]),
        ("neigh6", ["/usr/sbin/ip", "-6", "neigh", "show"]),
        ("rules4", ["/usr/sbin/ip", "-4", "rule", "show"]),
        ("rules6", ["/usr/sbin/ip", "-6", "rule", "show"]),
        ("routes4", ["/usr/sbin/ip", "-4", "route", "show", "table", "all"]),
        ("routes6", ["/usr/sbin/ip", "-6", "route", "show", "table", "all"]),
        ("iptables", ["/usr/sbin/iptables-save", "-c"]),
        ("nft", ["/usr/sbin/nft", "list", "ruleset"]),
        ("ipset", ["/usr/sbin/ipset", "save"]),
        ("sockets", ["/usr/bin/ss", "-Htunap"]),
        ("conntrack_src", conntrack_argv("-L", client, "-s")),
        ("conntrack_dst", conntrack_argv("-L", client, "-d")),
        ("arp", ["/usr/bin/cat", "/proc/net/arp"]),
        ("netdev", ["/usr/bin/cat", "/proc/net/dev"]),
    ]
    for idx, address in enumerate((client_ipv6 or [])[:4]):
        commands.append((f"conntrack6_src_{idx}", conntrack_argv("-L", address, "-s")))
        commands.append((f"conntrack6_dst_{idx}", conntrack_argv("-L", address, "-d")))
    ip6save = shutil.which("ip6tables-save")
    if ip6save:
        commands.insert(7, ("ip6tables", [ip6save, "-c"]))
    wg = shutil.which("wg")
    if wg:
        commands.append(("wireguard_endpoints", [wg, "show", "all", "endpoints"]))
        commands.append(("wireguard_handshakes", [wg, "show", "all", "latest-handshakes"]))
        commands.append(("wireguard_transfer", [wg, "show", "all", "transfer"]))
    awg = shutil.which("awg")
    if awg:
        commands.append(("amneziawg_endpoints", [awg, "show", "all", "endpoints"]))
        commands.append(("amneziawg_handshakes", [awg, "show", "all", "latest-handshakes"]))
        commands.append(("amneziawg_transfer", [awg, "show", "all", "transfer"]))
    return commands


def collect_phase(
    run_dir: Path,
    phase: str,
    client: str,
    client_ipv6: list[str] | None = None,
) -> list[dict]:
    results = []
    for name, argv in baseline_commands(client, client_ipv6):
        results.append(snapshot(run_dir, f"{phase}.{name}", argv))
    return results


def discover_client_ipv6(mac: str | None) -> tuple[list[str], str]:
    if not mac:
        return [], "mac_unavailable"
    try:
        result = subprocess.run(
            ["/usr/sbin/ip", "-6", "neigh", "show"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=2.0,
            check=False,
            text=True,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        return [], f"ipv6_neigh:{exc}"
    if result.returncode != 0:
        return [], f"ipv6_neigh_rc={result.returncode}"
    found: set[str] = set()
    expected = mac.lower()
    for line in result.stdout.splitlines():
        fields = line.split()
        if "lladdr" not in fields:
            continue
        pos = fields.index("lladdr")
        if pos + 1 >= len(fields) or fields[pos + 1].lower() != expected:
            continue
        try:
            address = ipaddress.ip_address(fields[0].split("%", 1)[0])
        except ValueError:
            continue
        if address.version != 6 or address.is_multicast or address.is_unspecified:
            continue
        found.add(str(address))
    ordered = sorted(
        found,
        key=lambda value: (ipaddress.ip_address(value).is_link_local, value),
    )
    return ordered[:4], ""


def build_capture_filter(client: str, mac: str | None, client_ipv6: list[str]) -> list[str]:
    # Capture all IPv6 in the bounded ring so a client privacy address that
    # appears after start cannot evade the trace. Post-run neighbor identity
    # maps observed IPv6 addresses back to the expected MAC before reporting.
    return ["(", "host", client, "or", "ip6", ")"]


def start_observer(
    argv: list[str],
    stdout_path: Path | None = None,
    stderr_path: Path | None = None,
    *,
    limit: int | None = None,
) -> subprocess.Popen:
    stdout = subprocess.DEVNULL
    stderr = subprocess.DEVNULL
    out_fh = err_fh = None
    if stdout_path is not None:
        out_fh = stdout_path.open("ab", buffering=0)
        stdout = out_fh
    if stderr_path is not None:
        err_fh = stderr_path.open("ab", buffering=0)
        stderr = err_fh
    proc = subprocess.Popen(
        argv,
        stdin=subprocess.DEVNULL,
        stdout=stdout,
        stderr=stderr,
        start_new_session=True,
        preexec_fn=file_limit(limit) if limit is not None else None,
    )
    proc._diag_files = (out_fh, err_fh)
    return proc


def stop_observer(proc: subprocess.Popen | None, *, grace: float = 2.0) -> int | None:
    if proc is None:
        return None
    if proc.poll() is None:
        try:
            os.killpg(proc.pid, signal.SIGINT)
        except ProcessLookupError:
            pass
        try:
            proc.wait(timeout=grace)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(proc.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                proc.wait(timeout=0.5)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                proc.wait(timeout=0.5)
    files = getattr(proc, "_diag_files", ())
    for fh in files:
        if fh is not None:
            try:
                fh.close()
            except OSError:
                pass
    return proc.returncode


def append_conntrack_sample(path: Path, clients: list[str]) -> None:
    if path.exists() and path.stat().st_size >= TEXT_LIMIT:
        return
    stamp = utc_now()
    chunks: list[bytes] = []
    for address in clients[:5]:
        try:
            result = subprocess.run(
                conntrack_argv("-L", address, "-s"),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                timeout=1.0,
                check=False,
            )
            payload = result.stdout[:64 * 1024]
        except (OSError, subprocess.SubprocessError) as exc:
            payload = f"ERROR address={address} {exc}\n".encode()
        chunks.append(f"--- {address} ---\n".encode() + payload)
    with path.open("ab") as fh:
        fh.write(f"=== {stamp} ===\n".encode())
        for payload in chunks:
            fh.write(payload)
            if not payload.endswith(b"\n"):
                fh.write(b"\n")


def pcap_files(run_dir: Path) -> list[Path]:
    return sorted(p for p in run_dir.glob("traffic.pcap*") if p.is_file())


def render_pcaps(run_dir: Path) -> list[dict]:
    results: list[dict] = []
    for idx, pcap in enumerate(pcap_files(run_dir)):
        results.append(
            snapshot(
                run_dir,
                f"pcap_text_{idx}",
                ["/usr/bin/tcpdump", "-nn", "-tttt", "-e", "-vv", "-r", str(pcap)],
                timeout=8.0,
                limit=TEXT_LIMIT,
            )
        )
    return results


FLOW_LINE_RE = re.compile(
    r"(?:^|\s)(?P<family>IP6|IP)\s+(?P<src>\S+)\s+>\s+(?P<dst>\S+)"
)
SLL2_HEADER_RE = re.compile(
    r"^\S+\s+\S+\s+(?P<iface>[A-Za-z0-9_.:-]+)\s+(?:In|Out)\s+.*ethertype\s+(?P<family>IPv6|IPv4)\s"
)
SLL2_FLOW_RE = re.compile(r"^\s+(?P<src>\S+)\s+>\s+(?P<dst>\S+)")
DNS_Q_RE = re.compile(r"\b(?:A|AAAA)\?\s+([A-Za-z0-9_.-]+)\.")
MARK_RE = re.compile(r"\bmark=(0x[0-9a-fA-F]+|\d+)\b")
IFACE_RE = re.compile(r"^(?:\S+\s+){1,2}(?P<iface>[A-Za-z0-9_.:-]+)\s+(?:In|Out)\s+")


def split_endpoint(token: str) -> tuple[str | None, str | None]:
    token = token.rstrip(":,")
    try:
        return str(ipaddress.ip_address(token)), None
    except ValueError:
        pass
    host, dot, port = token.rpartition(".")
    if not dot or not port.isdigit():
        return None, None
    try:
        return str(ipaddress.ip_address(host)), port
    except ValueError:
        return None, None


def line_protocol(line: str) -> str:
    if " UDP, " in line:
        return "udp"
    if " Flags [" in line:
        return "tcp"
    if " ICMP6" in line:
        return "icmp6"
    if " ICMP " in line:
        return "icmp"
    return "other"


def summarize(run_dir: Path, client: str, client_ipv6: list[str] | None = None) -> dict:
    client_addresses = {client, *(client_ipv6 or [])}
    remotes: dict[str, int] = {}
    remote_ipv6: dict[str, int] = {}
    ports: dict[str, int] = {}
    domains: dict[str, int] = {}
    interfaces: dict[str, int] = {}
    marks: dict[str, int] = {}
    protocols: dict[str, int] = {}
    packet_lines = 0
    ipv4_packet_lines = 0
    ipv6_packet_lines = 0
    quic_candidate_packets = 0

    for txt in sorted(run_dir.glob("pcap_text_*.txt")):
        try:
            lines = txt.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            continue
        pending_sll2: tuple[str, str] | None = None
        for line in lines:
            header = SLL2_HEADER_RE.match(line)
            if header:
                pending_sll2 = (
                    "IP6" if header.group("family") == "IPv6" else "IP",
                    header.group("iface"),
                )
                continue
            if " ethertype IPv4 " in line or " ethertype IPv6 " in line:
                fields = line.split()
                if len(fields) >= 4 and fields[3] in {"In", "Out"}:
                    pending_sll2 = (
                        "IP6" if " ethertype IPv6 " in line else "IP",
                        fields[2],
                    )
                    continue
            flow = FLOW_LINE_RE.search(line)
            family = flow.group("family") if flow else ""
            iface = ""
            if not flow and pending_sll2:
                flow = SLL2_FLOW_RE.match(line)
                if flow:
                    family, iface = pending_sll2
            pending_sll2 = None
            if not flow:
                continue
            src, sport = split_endpoint(flow.group("src"))
            dst, dport = split_endpoint(flow.group("dst"))
            if not src or not dst:
                continue
            if src not in client_addresses and dst not in client_addresses:
                continue
            packet_lines += 1
            if family == "IP6":
                ipv6_packet_lines += 1
            else:
                ipv4_packet_lines += 1
            if not iface:
                ifm = IFACE_RE.match(line)
                if ifm:
                    iface = ifm.group("iface")
            if iface:
                interfaces[iface] = interfaces.get(iface, 0) + 1
            protocol = line_protocol(line)
            protocols[protocol] = protocols.get(protocol, 0) + 1
            remote = dst if src in client_addresses else src
            port = dport if src in client_addresses else sport
            remotes[remote] = remotes.get(remote, 0) + 1
            try:
                if ipaddress.ip_address(remote).version == 6:
                    remote_ipv6[remote] = remote_ipv6.get(remote, 0) + 1
            except ValueError:
                pass
            if port:
                ports[port] = ports.get(port, 0) + 1
            if protocol == "udp" and port == "443":
                quic_candidate_packets += 1
            for domain in DNS_Q_RE.findall(line):
                domains[domain.lower()] = domains.get(domain.lower(), 0) + 1

    conntrack_paths = sorted(run_dir.glob("conntrack.events*.log"))
    samples = run_dir / "conntrack.samples.log"
    if samples.exists():
        conntrack_paths.append(samples)
    for path in conntrack_paths:
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for mark in MARK_RE.findall(text):
            marks[mark] = marks.get(mark, 0) + 1

    def top(data: dict[str, int], n: int = 30) -> list[dict]:
        return [
            {"value": key, "count": count}
            for key, count in sorted(data.items(), key=lambda item: (-item[1], item[0]))[:n]
        ]

    return {
        "schemaVersion": SCHEMA,
        "run_id": run_dir.name,
        "client": client,
        "client_ipv6": list(client_ipv6 or []),
        "packet_lines_parsed": packet_lines,
        "ipv4_packet_lines": ipv4_packet_lines,
        "ipv6_packet_lines": ipv6_packet_lines,
        "quic_candidate_packets": quic_candidate_packets,
        "protocols": top(protocols),
        "top_remote_ips": top(remotes),
        "top_remote_ipv6": top(remote_ipv6),
        "top_remote_ports": top(ports),
        "dns_queries": top(domains),
        "observed_interfaces": top(interfaces),
        "conntrack_marks": top(marks),
        "pcap_files": [{"name": p.name, "bytes": p.stat().st_size} for p in pcap_files(run_dir)],
        "notes": [
            "The run is passive: no route/firewall/DNS/DHCP/service mutation is performed.",
            "IPv6 coverage keeps all IPv6 in the bounded ring, then maps pre/post neighbor identities back to the expected client MAC before reporting.",
            "UDP/443 is reported as a QUIC candidate; encrypted application payload is intentionally not decrypted.",
            "A mark is kernel classification evidence; actual path must be cross-checked against captured interface plus saved rules/routes.",
        ],
    }


def write_summary(run_dir: Path, summary: dict) -> None:
    atomic_json(run_dir / "report.json", summary)
    lines = [
        f"DIAG traffic route {summary['run_id']}",
        f"client={summary['client']} packets={summary['packet_lines_parsed']} "
        f"ipv4={summary['ipv4_packet_lines']} ipv6={summary['ipv6_packet_lines']} "
        f"quic_candidates={summary['quic_candidate_packets']} pcap_files={len(summary['pcap_files'])}",
    ]
    for label, key in [
        ("interfaces", "observed_interfaces"),
        ("marks", "conntrack_marks"),
        ("protocols", "protocols"),
        ("remote_ips", "top_remote_ips"),
        ("remote_ipv6", "top_remote_ipv6"),
        ("remote_ports", "top_remote_ports"),
        ("dns", "dns_queries"),
    ]:
        values = summary[key][:10]
        rendered = ", ".join(f"{item['value']}({item['count']})" for item in values) or "-"
        lines.append(f"{label}: {rendered}")
    path = run_dir / "summary.txt"
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    os.chmod(path, 0o640)


STOP_REQUEST = "stop.request"
TERMINAL = {"SUCCESS", "FAILED", "CANCELLED"}


def pcap_bytes(run_dir: Path) -> int:
    return sum(p.stat().st_size for p in pcap_files(run_dir))


def arp_mismatch(client: str, expected_mac: str) -> str:
    """Observed MAC when the client IP now answers from another device; a
    briefly incomplete ARP entry is not a mismatch."""
    try:
        lines = Path("/proc/net/arp").read_text(encoding="utf-8", errors="replace").splitlines()[1:]
    except OSError:
        return ""
    for line in lines:
        f = line.split()
        if len(f) >= 6 and f[0] == client and f[2].lower() == "0x2":
            mac = f[3].lower()
            if mac not in ("00:00:00:00:00:00", expected_mac):
                return mac
    return ""


def pid_alive(pid: int, needle: str = "") -> bool:
    try:
        cmd = Path(f"/proc/{pid}/cmdline").read_bytes().replace(b"\0", b" ").decode("utf-8", "replace")
    except OSError:
        return False
    return not needle or needle in cmd


def cleanup_receipt(observers: dict) -> dict:
    alive = {n: pid_alive(p.pid) and p.poll() is None for n, p in observers.items() if p is not None}
    return {"checked_at": utc_now(), "observers_alive": alive, "all_stopped": not any(alive.values())}


def worker(args: argparse.Namespace) -> int:
    run_dir = Path(args.run_dir).resolve()
    client = args.client
    duration = int(args.duration)
    write_state(
        run_dir,
        state="RUNNING",
        started_at=utc_now(),
        client=client,
        mac=args.mac or "",
        duration_seconds=duration,
        pid=os.getpid(),
        passive_only=True,
    )

    errors: list[str] = []
    observers: dict[str, subprocess.Popen | None] = {"tcpdump": None}
    try:
        identity = verify_arp_identity(client, args.mac)
        client_ipv6, ipv6_discovery_error = discover_client_ipv6(args.mac)
        capture_filter = build_capture_filter(client, args.mac, client_ipv6)
        write_state(
            run_dir,
            target_identity=identity,
            client_ipv6=client_ipv6,
            ipv6_discovery_error=ipv6_discovery_error,
            capture_filter=" ".join(capture_filter),
        )
        pre = collect_phase(run_dir, "pre", client, client_ipv6)

        tcpdump_stderr = run_dir / "tcpdump.stderr.log"
        tcpdump_argv = [
            "/usr/bin/tcpdump",
            "-i", "any",
            "-nn",
            "-U",
            "-s", str(SNAPLEN),
            "-B", "4096",
            "-Z", "root",
            "-w", str(run_dir / "traffic.pcap"),
            *capture_filter,
        ]
        try:
            observers["tcpdump"] = start_observer(tcpdump_argv, stderr_path=tcpdump_stderr, limit=PCAP_BYTE_CAP)
        except OSError as exc:
            errors.append(f"tcpdump_start:{exc}")

        client_addresses = [client, *client_ipv6]
        for idx, address in enumerate(client_addresses[:5]):
            observer_name = "conntrack" if idx == 0 else f"conntrack6_{idx - 1}"
            events_name = "conntrack.events.log" if idx == 0 else f"conntrack.events6_{idx - 1}.log"
            stderr_name = "conntrack.stderr.log" if idx == 0 else f"conntrack.stderr6_{idx - 1}.log"
            try:
                observers[observer_name] = start_observer(
                    conntrack_argv("-E", address, "-s", "timestamp,extended"),
                    stdout_path=run_dir / events_name,
                    stderr_path=run_dir / stderr_name,
                    limit=TEXT_LIMIT,
                )
            except OSError as exc:
                errors.append(f"{observer_name}_start:{exc}")

        write_state(run_dir, observer_pids={n: p.pid for n, p in observers.items() if p is not None})
        deadline = time.monotonic() + duration
        next_mac_check = time.monotonic() + MAC_RECHECK_SECONDS
        samples = run_dir / "conntrack.samples.log"
        ended = "duration"
        while time.monotonic() < deadline:
            if (run_dir / STOP_REQUEST).exists():
                ended = "cancelled"
                break
            tcp = observers["tcpdump"]
            if tcp is not None and tcp.poll() is not None:
                ended = "byte_cap" if pcap_bytes(run_dir) >= PCAP_BYTE_CAP - 65536 else "tcpdump_exited"
                break
            if args.mac and time.monotonic() >= next_mac_check:
                next_mac_check = time.monotonic() + MAC_RECHECK_SECONDS
                observed = arp_mismatch(client, args.mac)
                if observed:
                    errors.append(f"target_identity_mismatch_during_capture: observed={observed}")
                    ended = "mac_mismatch"
                    break
            append_conntrack_sample(samples, client_addresses)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            time.sleep(min(2.0, remaining))

        tcp_rc = stop_observer(observers["tcpdump"])
        ct_rc = {
            name: stop_observer(proc)
            for name, proc in observers.items()
            if name != "tcpdump"
        }
        cleanup = cleanup_receipt(observers)
        post_ipv6, post_ipv6_error = discover_client_ipv6(args.mac)
        all_client_ipv6 = sorted(
            set(client_ipv6) | set(post_ipv6),
            key=lambda value: (ipaddress.ip_address(value).is_link_local, value),
        )[:4]
        post = collect_phase(run_dir, "post", client, all_client_ipv6)
        rendered = render_pcaps(run_dir)
        summary = summarize(run_dir, client, all_client_ipv6)
        write_summary(run_dir, summary)
        write_state(
            run_dir,
            client_ipv6=all_client_ipv6,
            post_ipv6_discovery_error=post_ipv6_error,
        )

        if not pcap_files(run_dir):
            errors.append("no_pcap_files")
        snapshot_errors = [
            f"{item['name']}:{item['error']}"
            for item in pre + post + rendered
            if item.get("error")
        ]
        errors.extend(snapshot_errors)
        final_state = "SUCCESS" if pcap_files(run_dir) else "FAILED"
        if ended == "cancelled":
            final_state = "CANCELLED"
        elif ended == "mac_mismatch":
            final_state = "FAILED"
        write_state(
            run_dir,
            state=final_state,
            ended_by=ended,
            pcap_bytes=pcap_bytes(run_dir),
            pcap_byte_cap=PCAP_BYTE_CAP,
            cleanup=cleanup,
            finished_at=utc_now(),
            tcpdump_rc=tcp_rc,
            conntrack_rc=ct_rc,
            errors=errors,
            report="report.json",
            summary="summary.txt",
        )
        return 0 if final_state == "SUCCESS" else 1
    except BaseException as exc:
        for proc in observers.values():
            stop_observer(proc)
        write_state(
            run_dir,
            state="FAILED",
            cleanup=cleanup_receipt(observers),
            finished_at=utc_now(),
            errors=errors + [f"worker:{type(exc).__name__}:{exc}"],
        )
        return 1


def start(args: argparse.Namespace) -> int:
    if os.geteuid() != 0 and not args.dry_run:
        print(
            "traffic start requires root/CAP_NET_ADMIN+CAP_NET_RAW; use --dry-run for preflight",
            file=sys.stderr,
        )
        return 2
    if not MIN_DURATION <= args.duration <= MAX_DURATION:
        print(f"duration must be {MIN_DURATION}..{MAX_DURATION}s", file=sys.stderr)
        return 2

    root = Path(args.root).resolve()
    run_id = safe_run_id()
    run_dir = root / run_id

    if args.dry_run:
        plan = {
            "schemaVersion": SCHEMA,
            "mode": "dry-run",
            "client": args.client,
            "mac": args.mac or "",
            "duration_seconds": args.duration,
            "run_root": str(root),
            "pcap_budget_bytes": PCAP_BYTE_CAP,
            "snaplen": SNAPLEN,
            "collectors": [
                "ip rules/routes/links",
                "iptables/ip6tables counters",
                "nft ruleset",
                "conntrack IPv4/IPv6 pre/events/samples/post",
                "ARP/IPv6-neigh/netdev",
                "tcpdump any client-IPv4 + bounded-all-IPv6, one file, hard byte cap (no ring overwrite)",
                "offline bounded tcpdump rendering with IPv4/IPv6/QUIC summary",
            ],
            "mutations": [],
        }
        print(json.dumps(plan, ensure_ascii=False, indent=2))
        return 0

    ensure_run_root(root)
    run_dir.mkdir(mode=0o2770)
    try:
        os.chmod(run_dir, 0o2770)
    except PermissionError:
        pass
    write_state(
        run_dir,
        state="PENDING",
        created_at=utc_now(),
        client=args.client,
        mac=args.mac or "",
        duration_seconds=args.duration,
        passive_only=True,
    )

    argv = [
        sys.executable,
        str(Path(__file__).resolve()),
        "_worker",
        "--run-dir", str(run_dir),
        "--client", args.client,
        "--duration", str(args.duration),
    ]
    if args.mac:
        argv.extend(["--mac", args.mac])

    log = (run_dir / "worker.log").open("ab", buffering=0)
    proc = subprocess.Popen(
        argv,
        stdin=subprocess.DEVNULL,
        stdout=log,
        stderr=subprocess.STDOUT,
        start_new_session=True,
        close_fds=True,
    )
    log.close()
    write_state(run_dir, state="RUNNING", pid=proc.pid, started_at=utc_now())
    print(f"RUN {run_id} state=RUNNING pid={proc.pid} path={run_dir}")
    return 0


def resolve_run(root: str, run_id: str) -> Path:
    if "/" in run_id or run_id in {".", ".."}:
        raise ValueError("invalid run id")
    run_dir = Path(root).resolve() / run_id
    if not run_dir.is_dir():
        raise FileNotFoundError(run_id)
    return run_dir


def status(args: argparse.Namespace) -> int:
    try:
        run_dir = resolve_run(args.root, args.run)
        data = json.loads(state_path(run_dir).read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"UNKNOWN run={args.run} error={exc}", file=sys.stderr)
        return 3
    if args.json:
        print(json.dumps(data, ensure_ascii=False, indent=2))
    else:
        print(
            f"RUN {data.get('run_id', args.run)} state={data.get('state', 'UNKNOWN')} "
            f"client={data.get('client', '?')} pid={data.get('pid', '?')} "
            f"updated={data.get('updated_at', '?')}"
        )
    return 0 if data.get("state") in {"SUCCESS", "RUNNING", "PENDING", "CANCELLED"} else 1


def stop(args: argparse.Namespace) -> int:
    """Idempotent cancel: ask the worker to stop; if it is gone or stuck,
    kill its process groups (verified by cmdline) and write the receipt."""
    try:
        run_dir = resolve_run(args.root, args.run)
        data = json.loads(state_path(run_dir).read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"UNKNOWN run={args.run} error={exc}", file=sys.stderr)
        return 3
    if data.get("state") not in TERMINAL:
        (run_dir / STOP_REQUEST).write_text(utc_now() + "\n", encoding="utf-8")
        deadline = time.monotonic() + STOP_WAIT_SECONDS
        while time.monotonic() < deadline:
            data = json.loads(state_path(run_dir).read_text(encoding="utf-8"))
            if data.get("state") in TERMINAL:
                break
            time.sleep(0.5)
    if data.get("state") not in TERMINAL:
        targets = [(int(data.get("pid") or 0), "_worker")]
        targets += [(int(pid), name.rstrip("_0123456789").replace("conntrack6", "conntrack")) for name, pid in (data.get("observer_pids") or {}).items()]
        for pid, needle in targets:
            if pid > 1 and pid_alive(pid, needle):
                for sig in (signal.SIGTERM, signal.SIGKILL):
                    try:
                        os.killpg(pid, sig)
                    except ProcessLookupError:
                        break
                    time.sleep(0.5)
                    if not pid_alive(pid, needle):
                        break
        alive = {str(pid): pid_alive(pid, needle) for pid, needle in targets if pid > 1}
        data = write_state(
            run_dir,
            state="CANCELLED",
            ended_by="forced_stop",
            pcap_bytes=pcap_bytes(run_dir),
            pcap_byte_cap=PCAP_BYTE_CAP,
            cleanup={"checked_at": utc_now(), "observers_alive": alive, "all_stopped": not any(alive.values())},
            finished_at=utc_now(),
        )
    print(json.dumps(data, ensure_ascii=False, indent=2))
    return 0 if (data.get("cleanup") or {}).get("all_stopped", True) else 1


def report(args: argparse.Namespace) -> int:
    try:
        run_dir = resolve_run(args.root, args.run)
    except (OSError, ValueError) as exc:
        print(f"UNKNOWN run={args.run} error={exc}", file=sys.stderr)
        return 3
    path = run_dir / ("report.json" if args.json else "summary.txt")
    try:
        sys.stdout.write(path.read_text(encoding="utf-8"))
    except OSError as exc:
        print(f"report unavailable: {exc}", file=sys.stderr)
        return 3
    return 0


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="bounded passive route observer for one LAN client")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("start")
    s.add_argument("--client", required=True, type=valid_client)
    s.add_argument("--mac", type=valid_mac)
    s.add_argument("--duration", type=int, default=DEFAULT_DURATION)
    s.add_argument("--root", default=str(default_runs_root()))
    s.add_argument("--dry-run", action="store_true")
    s.set_defaults(func=start)

    s = sub.add_parser("status")
    s.add_argument("--run", required=True)
    s.add_argument("--root", default=str(default_runs_root()))
    s.add_argument("--json", action="store_true")
    s.set_defaults(func=status)

    s = sub.add_parser("stop")
    s.add_argument("--run", required=True)
    s.add_argument("--root", default=str(default_runs_root()))
    s.add_argument("--json", action="store_true")
    s.set_defaults(func=stop)

    s = sub.add_parser("report")
    s.add_argument("--run", required=True)
    s.add_argument("--root", default=str(default_runs_root()))
    s.add_argument("--json", action="store_true")
    s.set_defaults(func=report)

    s = sub.add_parser("_worker")
    s.add_argument("--run-dir", required=True)
    s.add_argument("--client", required=True, type=valid_client)
    s.add_argument("--mac", type=valid_mac)
    s.add_argument("--duration", type=int, required=True)
    s.set_defaults(func=worker)

    return p


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
