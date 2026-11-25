# MCPshka 0.1.59 — P5 Docker observability hotfix

Accepted: 2026-10-08. Production deployment `1791438800489-cf2f0d23ff08` succeeded through the guarded detached updater.
Installed: `0.1.59`, SHA-256 `006b5e6f6882914d05fa36121f79a192f725e87bfed995d364da5594024b2b14`.
Rollback: `0.1.58`, SHA-256 `1a5ca626bd8e4740340610ccd688ce72eec3687bed739996c968114b87bb105f`.

## Source isolation and protected core

The deployed binary comes from clean branch `release/p5-docker-0.1.59`, commit
`2c132f1fa0cf3e034c622dd84a8be48f58702e64`, based on accepted 0.1.58
commit `f82bfe9`. The only changes relative to 0.1.58 were
`internal/dockerops/docker.go`, `internal/dockerops/docker_test.go`,
and the version constant in `internal/mcpserver/server.go`.
Unaccepted P4 rescue/root-broker/systemd changes in development `main`
are NOT included in the installed binary, and no rescue timer or broker
installation was performed.

## Acceptance

- Full `go test -p 1 ./...` passed in the isolated release worktree.
- `docker_list` returns 13 entries: 11 running, 2 intentionally exited.
  Its formerly fatal Docker CLI warning is captured as bounded diagnostic evidence.
- `docker_status(homeassistant)` returned running/healthy.
  `docker inspect` now tolerates bounded warning lines but rejects invalid,
  duplicated, absent, or warning-only JSON status output.
- Main service/tunnel health and ready, SQLite state, DNS and HTTPS passed.
  Deployed and rollback binary checksums match the expected images.
- `project_verify` passed installed version, rollback, service, ready,
  DNS and HTTPS checks but reports `git_clean=false`: unrelated prior
  modified `README.md` and untracked `error.log` deliberately remain.
  These files were not altered or removed by this release.

Remaining P5 follow-up: assess Docker's underlying warning about denied
`/root/.docker/config.json` read without exposing credentials or changing
Docker configuration. P4 still requires independent verified OOB paths;
Tier 1 engineering/soak gates are not complete.
