# MCPshka 0.1.156

Paired controller + broker fix release on top of 0.1.155. The second live
integration run reached the whole-module test but was cut off; these fixes let
it complete and be retried.

## Fixes

- **The worker no longer cuts a whole-module test at 30 minutes
  (BUG-20261021-10).** `executor-worker` used a fixed 1790 s context for every
  profile, below the module unit's one-hour `RuntimeMaxSec`; the test stopped
  inside `mcpserver` with every finished package passing. The context is now
  3590 s; each unit's `RuntimeMaxSec` still bounds its profile.
- **A commit can be tested again (BUG-20261021-6).** Preparation derived one
  permit per repository and commit, and a permit runs once, so a failed or cut
  run blocked that commit forever. `integration_test_prepare` takes `attempt`
  (1–9); attempt 1 keeps the previous identity.
- **Less starvation.** Module test units keep `Nice=19` and idle IO but drop
  `CPUWeight=1`: together they starved the test under host load.

## Next

The integration gate should accept verified GitHub CI evidence for the exact
commit tree instead of a local whole-module test (plan in ROADMAP, 0.1.156+).
