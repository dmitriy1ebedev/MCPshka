# Targeted tests and bounded job observation

`project_test` accepts optional `packages` and `run` filters for Go modules. For example:

```json
{"path":"/path/to/project","packages":["./internal/jobs"],"run":"^TestHeavyJob","idempotency_key":"targeted-check-1"}
```

Packages must be module-relative (`.`, `./internal/jobs`, or a trailing `/...` pattern). Flags, shell syntax, traversal and resolved paths outside the module are rejected. Filters select Go directly even when the project also has a Makefile. Without filters, the existing automatic strategy is unchanged. Filters are included in the idempotency fingerprint.

`job_status` returns compact status by default: zero timestamps, boot identity, start ticks and default resource limits are omitted. Custom limits remain visible. Pass `verbose: true` for the complete snapshot.

`job_wait` accepts `job_id` and `timeout_seconds` from 0 to 120. It returns on a state change, job completion, or timeout, with compact `status` and at most 1024 bytes each of stdout and stderr tails. Zero means an immediate observation. Context cancellation stops waiting. Receipt ownership checks are the same as for `job_status` and `job_output`.

Native `project_test`, `project_build`, and direct `go test`/`go build` jobs run with nice level 19 and idle I/O priority. When systemd scopes are enabled, scheduling is applied inside the scope while existing cgroup limits remain in force. Scheduling overrides are internal only. This does not change external executor runtimes.

The jobs reaper also checks the service-user Go build cache. Above 1 GiB it runs `go clean -cache` only while all registered jobs are idle and no admission is pending; new native job admission is held during maintenance. Scanning is capped at 100000 entries and five seconds, cleaning at twenty seconds. Start, completion and failure are audited. The module cache is unaffected.
