# H96 SSH MOTD (MCP-173)

- SSH/PAM login executes `router-motd-login` and reads a small cached file.
- The login path does **not** run a collector, a network probe, or systemd.
- `router-motd-refresh.timer` refreshes the cache on a low-priority schedule.
- Login output is **six lines, <=48 visible columns**, with fixed ANSI SGR colors
  (cyan labels, green OK, yellow WARN, red CRIT, dim command hint).
- The background timer prepares **two root-owned bounded snapshots**:
  `/run/router-motd/snapshot` (color) and `snapshot.plain`.
- The login hook performs only one bounded `head -c 1024` read. It does **not**
  spawn systemd or collectors, run iconv/awk, or block on network / Lane.
  `TERM=dumb`, `NO_COLOR`, or `ROUTER_MOTD_COLOR=never` chooses plain output;
  `ROUTER_MOTD_COLOR=always` explicitly forces ANSI for capable terminals.
- Thresholds for the compact warning line: CPU >=65C WARN, >=75C CRIT;
  swap >=70% WARN, >=85% CRIT; disk >=85% WARN, >=92% CRIT.
  Service/MCP/Lane/Tailscale failures also appear as FIX warnings.
- Container/service health comes from the existing **full `motd` dashboard**:
  it already distinguishes running/healthy from exited, restarting and unhealthy;
  a deliberately stopped container with restart policy `no` and successful exit
  (code 0 or 143) does not create a false alarm.
  Login displays `svc N/N ok` when all counted services are healthy.
  On failure: `svc N/M err` and the first failed **container or service name**
  in `FIX`, followed by `+K` if other failures exist; run `motd` to see all names.
  Failed-service names take precedence over CPU/swap warnings when space is tight.
  Warnings never run remedial commands automatically.
- `motd` remains the detailed manual dashboard command.
- `/etc/router-motd.off` suppresses the hook output if created for an A/B test.
- The scripts never restart sshd, networking, Tailscale or Lane.

## Tests

```sh
go test -p 1 ./packaging/native/h96/motd
```

## Upgrade from the existing MCP-173 installation

Requires root and the owner's Face ID / approved task. Do not bypass the broker.

```sh
bash packaging/native/h96/motd/install-v2.sh status
bash packaging/native/h96/motd/install-v2.sh apply
```

The installer saves the **previous deployed files** under
`/var/backups/router-motd/mcp173-v2-*` and enables only the dedicated
refresh timer. It does not restart SSH or the network.

On failure it attempts automatic rollback. Manual rollback:

```sh
bash packaging/native/h96/motd/install-v2.sh rollback
```

This restores the previous login hook, refresher and any prior systemd unit
files. The original pre-MCP-173 backup remains intact.

The v1 `install.sh` is retained for compatibility, but must not be used to
reinstall over a deployed v2 without a deliberate rollback.

## Checks

```sh
systemctl is-active router-motd-refresh.timer
systemctl status --no-pager router-motd-refresh.service
run-parts --test /etc/update-motd.d
sh /etc/update-motd.d/32-router-dashboard
sshd -t
```

For a real client acceptance check: open **a new SSH session** from Shell Drop
and verify the banner fits the phone, login is immediate, and `tmux` still works.
A successful server-side test does not prove an iOS Termius crash is resolved.
