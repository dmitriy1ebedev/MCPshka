# MCPshka native packaging

The native service is intentionally local-only.

## Install

Build first:

    go build -trimpath -o bin/mcpshka ./cmd/mcpshka

Then, as root:

    packaging/native/install.sh

The installer:

- validates the new binary before replacing the current one;
- saves the prior binary as /usr/local/bin/mcpshka.previous;
- creates /etc/mcpshka/mcpshka.env with mode 0600 on first install;
- binds MCP HTTP to 127.0.0.1:8765;
- verifies and installs the systemd unit;
- enables, restarts and checks the service.

The bearer token is never printed by the installer. Read it from the root-only env file only when configuring a trusted connector or tunnel.

## Hardening

The systemd unit uses:

- loopback-only application binding;
- NoNewPrivileges;
- empty capability bounding and ambient capability sets;
- ProtectSystem=strict;
- protected kernel tunables/modules/logs/control groups;
- PrivateTmp and PrivateDevices;
- KillMode=mixed: SIGTERM goes to MCPshka first so it can mark/cancel jobs cleanly; remaining cgroup processes are still force-killed on stop timeout;
- MemoryMax=1536M;
- TasksMax=128;
- CPUQuota=250%;
- writable paths limited to the MCPshka state area and authorized project/control roots.

Jobs also have application-level output, concurrency and wall-clock bounds.

The service still runs as root in this alpha because the existing repositories are under /root/repos. Do not expose the listener directly to the public Internet. Moving execution to a dedicated mcpshka user with a narrow privileged broker remains a roadmap requirement before a public relay is considered production-safe.

## Guarded self-deploy

The `project_deploy` MCP tool can update MCPshka without Termius or a remote-desktop control plane. It accepts only a validated `mcpshka-*.candidate` directly under this repository's `bin/` directory and requires an idempotency key.

A detached transient systemd helper waits for the MCP response to return, preserves the current production binary as rollback, atomically swaps the candidate, restarts only `mcpshka.service`, and checks the service, tunnel, local health endpoints, DNS and outbound HTTPS. Failed verification automatically restores the prior binary. Deployment state is written under `/var/lib/mcpshka/deploys` and is available through `project_deploy_status`.

Generic `systemd-run` remains a privileged command in the normal execution policy; the self-deploy route is intentionally narrow.

## Rollback

    packaging/native/rollback.sh

Rollback verifies the previous binary, swaps it atomically, restarts the service and restores the former current binary if the rollback target fails to start.

## Uninstall

    packaging/native/uninstall.sh

Uninstall removes the service and current binary but deliberately preserves configuration, state, audit logs and the previous binary.
