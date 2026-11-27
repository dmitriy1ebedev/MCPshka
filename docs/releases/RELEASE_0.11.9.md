# MCPshka 0.11.9 - H96 bounded CPU for AI work

Status: CANDIDATE; no production deployment is implied by this file.

## Changes
- CPUQuota=150% for future per-attempt Codex/Claude systemd units and roadmap-run units.
- Preserve existing CPUWeight=20, Nice=10, MemoryHigh=2G, MemoryMax=3G, TasksMax=512 and bounded Go parallelism.
- Add explicit contract tests for the quota in agent and roadmap runner units.
- Include the reproducible H96 2026-11-12 baseline at docs/h96/2026-11-12-baseline.md.

## Operational intent
Limit the CPU used by one heavy AI task family while allowing HA, Zigbee2MQTT, MQTT, Lane, Tailscale, SSH and MCPshka to keep operating. This is not a guarantee against total host saturation: other jobs and unrelated containers need separate observation and limits.

## Verification
- Targeted tests TestQuotaAttemptUnitLifetimeAndBounds and TestRoadmapRunContract passed on the pre-version-bump commit 685bfa16ea1c2a49829f8e51b5ffa432b16209e5.
- Require a sealed full-module exact-HEAD test, protected integration, paired ARM64 images and guarded deployment before marking production ready.
- After deployment: verify controller and broker version 0.11.9, matched hashes, tunnel reconnect, HA/Zigbee/MQTT/DNS/Lane/Tailscale health, CPU quota for a freshly created canary AI job unit and rollback readiness.
- Compare matched-load CPU saturation, SSH connection latency and memory/swap activity with baseline before claiming performance improvement.

## Rollback
Do not copy binaries or restart services manually. Use MCPshka's guarded paired controller+broker release rollback; verify that the previous production version and companion broker resume with working management tunnel. New unit CPUQuota applies only to future units, not existing running jobs.

## Non-goals
No swapoff, zram reset, activation of the existing disabled 4-GiB /swapfile, remount/reboot, Docker restart, network/DNS/route changes, or automatic cleanup of production artifacts.
