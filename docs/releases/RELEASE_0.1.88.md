# 0.1.88 combined integration candidate

This batch preserves all six local branch histories in main. P9 retains the
production native DIAG provider and strong main ledger guards, while merging
the bounded CLI prototype and legacy unbound-record recovery tests. P10 keeps
the canonical SQLite knowledge store and pre-match secret filtering. P11 adds
a coherent export-opt-in snapshot adapter, revision freshness checks,
cancellation and provenance regressions without introducing a second store.

Voluntary project_work_yield atomically releases the pinned current holder
with a durable minimal checkpoint, under exclusive mutation admission and
fresh jobs/workflow/deployment/Git checks. Lost replies can be recovered through
principal-scoped proof; stale generation, wrong execution and unknown effects
hold. It does not forcibly fence or transfer legacy owners.

interaction_recovery_diagnose provides bounded read-only recovery guidance,
distinguishing DIAG permission failures from missing test configs and retaining
HOLD for workflow claims without jobs. Logs and client reports are untrusted
clues, never mutation authority. See INTERACTION_RECOVERY_DIAGNOSIS.md and
WORK_SEGMENT_COMPLETION.md.

Production acceptance is recorded separately after the combined tests, vet,
clean committed build, guarded updater, actual version/hash verification,
rollback readiness and cleanup. These source changes alone do not mark P9,
P10, P11, executor fencing or administrative transfer DONE. Companion extension
integration remains unconfirmed until its original source file is available.
