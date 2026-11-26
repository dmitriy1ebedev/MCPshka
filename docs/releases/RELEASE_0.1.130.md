# 0.1.130 — fixed core activation and recovery

`packaging/native/core129-upgrade.py` prepares an independently approved
transition for exactly the broker and two release helpers built and tested in
129. It pins the manifest, all previous/candidate component hashes, and the
running 129 frontend. It cannot accept caller paths, shell commands, a different
bundle or a different frontend. It is not registered as an MCP privilege grant.

The operator uses a reviewed root-owned copy at
`/usr/local/libexec/mcpshka-core129-upgrade.py`. `prepare` seals the three
candidates, three verified backups and manifest in the private executor state.
`approve <manifest_sha256>` is a separate, independently root-authorized human
step; neither CI nor MCP can issue it. It records no claim of biometric proof.
Legacy Home Assistant WebAuthn consent cannot authorize this bootstrap.

`deploy` persists a dispatch intent before starting a resource-bounded detached
systemd worker. Stopping the broker can stop its controller/tunnel dependents;
the worker survives independently and restores both, waiting for controller
health before starting the tunnel. Session/admission/deployment/HA locks exclude
concurrent effects. Approval, sealed inputs and worker sequence are rechecked
before mutation. An old queued worker cannot consume a later dispatch.

`status` observes the same transition after reconnect. A lost launch reply or a
hard crash remains UNKNOWN; repeating deploy does not submit another worker.
An interrupted preparation without a receipt reports
`PREPARATION_INCOMPLETE_HOLD`. It has no activation authority: preserve its root
snapshots for independent operator inspection; repeated prepare cannot overwrite
them. Preparation does not change installed component bytes.
After inspection, explicit `rollback` restores only the pinned previous bytes.
If that rollback itself was interrupted, `recover-rollback <observed_sequence>`
requires the previous worker to be absent and starts a fresh rollback sequence;
the old queued worker is fenced. Ordinary repeated rollback remains observation.
Ordinary failures attempt automatic rollback of all three components and verify
their health. Foreign bytes, unsafe files, active grants/workers or conflicting
locks keep HOLD. Root backups and receipts are retained, including failed ones.
Once any permanent deployment fence exists, this migration refuses to restore
the old fence-unaware helpers; a downgrade then needs a separately reviewed plan.
`verify` checks terminal component hashes, the running broker, protocol,
frontend health and tunnel readiness.

This is a one-time migration while frontend 129 is installed. Prepare the 130
frontend release plan only after the independently approved helper installation,
so its helper hash is current. Publishing or deploying frontend 130 does not
install the core bundle, mint an administrative grant or fence the historical
UNKNOWN deployment. Real owner Face ID acceptance remains a separate live check.
