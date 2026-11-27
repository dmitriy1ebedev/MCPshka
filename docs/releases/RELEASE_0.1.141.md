# 0.1.141 - Contain Workbench snapshot writes

Committed-source preparation now uses an os.Root descriptor for directory and
file creation. Directory permissions are set through opened descriptors, so a
destination symlink cannot redirect a chmod outside the private workspace.

Previously, a destination ancestor symlink could create external files and
change external directory modes before the final snapshot scan rejected it.
Regressions reproduce that ordering and require zero external entries and
unchanged directory permissions for absolute and relative links at two depths.
Normal pinned dependency preparation and source worktree preservation remain
covered by existing tests. No dependency or command/permit interface is added.

This is a bounded P2 containment fix, not full Workbench sandbox acceptance.
The root-only constructor still creates its fresh private base. Root paths and
mount topology remain trusted; os.Root does not prohibit bind-mount traversal.
Existing source validation, byte/file limits and final digest checks remain.

This candidate includes the prior 0.1.140 search-effects fix. Neither candidate
has been activated; production remains 0.1.139 until separate protected-core
approval and the existing guarded release gates are satisfied.

Verification: the focused snapshot/dependency/link tests pass on H96. The native
full executor suite fails in separate candidate/fencing checks; one identical
failure was reproduced using the previous implementation from commit 9933d70
through a temporary Go overlay. That run is not a passing package gate. Full
fixed-source cloud release/race/fuzz/build/release-arm64 gates remain required.
Independent review found no actionable issues in this bounded change.
