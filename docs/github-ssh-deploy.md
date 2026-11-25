# GitHub SSH deploy-key integration (isolated)

The MCPshka runtime runs as the unprivileged `mcpshka` service account (UID 994), with HOME=/nonexistent. It cannot use `/root/.ssh` or persist host keys there. Do not widen permissions on `/root` or disable SSH host verification.

## Required host provisioning (outside the unprivileged MCP tool)

1. Provision a dedicated directory owned by `mcpshka`, mode 0700, e.g. `/var/lib/mcpshka/git-ssh`, via the authorized guarded host administrator. Do not commit credentials.
2. Install a **dedicated** GitHub deploy private key as `identity`, mode 0600, owned by `mcpshka`. Register only its matching public key in the `dmitriy1ebedev/mcpshka` repository Deploy keys, with write access. Do not copy arbitrary `/root/.ssh` identities.
3. Populate `known_hosts` using GitHub's **officially published** ED25519 host key fingerprint, verified out-of-band; set mode 0600. Do not use `StrictHostKeyChecking=no` or unverified `ssh-keyscan` output.
4. Run the pinned preflight with `python3 packaging/gitverse/github_ssh_preflight.py --connect`, then use `GIT_SSH=<absolute-repo>/packaging/gitverse/github_ssh_transport.sh` for Git. The fixed wrapper preserves strict host checking and avoids a shell-parsed SSH command.
5. Verify `git ls-remote git@github.com:dmitriy1ebedev/mcpshka.git HEAD` using the service account. Empty stdout with exit 0 can be valid for an empty remote.
6. Under a project writer lease, add `github` remote (preserve GitVerse `origin`), inspect for secrets, and push only the intended reviewed branch and tags. Verify remote SHA after push.

## Safety gates

No production network, VPN, DNS, Docker or SSH daemon changes. Never include the private key in artifacts, logs, repository, environment dumps, or AI responses. Do not modify `main` while another writer holds its lease. No automatic push before host identity, deploy-key permissions and secret audit pass.

## Current status

On 2026-10-15 the existing service-owned key and published host pin passed all
five preflight tests and a real GitHub connection. Fetch of the private repository
succeeded through the fixed wrapper. No key material was read or copied and no
SSH policy or credential permissions were changed. Publication still requires
a reviewed, checked snapshot and normal branch/release gates.
