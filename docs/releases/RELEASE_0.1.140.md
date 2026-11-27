# 0.1.140 - Tier 1 P2 search command effects

Candidate only. Protected policy activation requires separate human approval.

The shared command classifier no longer labels every find or rg invocation
read_only. Find deletion is destructive; command execution (-exec, -execdir,
-ok, -okdir) and output-file actions (-fprint, -fprint0, -fprintf, -fls) are
mutating. Ripgrep --pre and --pre= are mutating. Reader profiles therefore
reject these operations before a process is created. The existing destructive
approval path applies to find deletion under the operator profile.

Option operands, find command payloads and ripgrep's end-of-options marker
remain data. Literal names or patterns such as -delete and --pre do not acquire
authority or change a normal search into a destructive operation.

This is advisory risk routing, not an execution sandbox or transitive program
analysis. Operator-admitted interpreters and build tools still execute project
code; workbench isolation, arbitrary nested effects, configured hooks and
sandbox-escape acceptance remain open P2 work. No P2 or Tier 1 DONE claim.

Regression evidence:
- Before the fix, policy and real jobs-manager regressions failed: reader
  searches could launch commands with side effects in isolated temporary dirs.
- Final focused tests cover action ordering, literal arguments, clustered rg
  flags and rejection before find can remove or write test fixture files.
- Independent review found and then verified fixes for literal operand parsing.
- The native H96 all-package attempt encountered pre-existing bin/go-build
  source discovery and artifact inbox tests requiring a larger temporary
  filesystem reserve. It is not passing release evidence. Fixed-source cloud
  release/race/fuzz/build/release-arm64 checks remain required for activation.

Production baseline is 0.1.139, rollback 0.1.138. This source change does not
activate policy, replace the broker, alter network state or reclaim leases.
