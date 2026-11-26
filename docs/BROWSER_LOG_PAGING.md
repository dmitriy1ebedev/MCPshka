# Browser HTTP policy and Docker log pages

`browser_run` uses HTTPS for public sites. Private HTTP requires an operator
entry in `/etc/mcpshka/mcpshka.env`, loaded by the MCPshka service:

```text
MCPSHKA_BROWSER_HTTP_ALLOWLIST=192.168.77.10:8123
```

The default allowlist is empty. Entries are comma-separated literal private or
loopback IP addresses with an exact port; public IPs and DNS names cannot enable
HTTP. The tool's `site` is the same endpoint (`192.168.77.10:8123`). Redirects and
page requests are checked before sending. A run cannot supply its own allowlist.
Changes to the service environment take effect on its next approved restart.

Each site keeps its existing private Chromium profile under
`/var/lib/mcpshka/browser/profiles/`. Cookies and localStorage, including an
existing Home Assistant session, persist there. No session values are returned.
Expired or missing HA authentication is reported as `failure_stage: auth`.
The optional `local_storage` input imports an owner-exported map (such as HA
`hassTokens`) once, on the first goto origin, before the app loads. Cookie and
localStorage import, click and type require the administrative session.

Browser time budgets are independent:

- `process_timeout_seconds`: default 170, range 5–300; the outer job adds ten
  seconds for shutdown and the structured result.
- `launch_timeout_ms`: default 30000, maximum 60000 for Chromium/CDP startup.
- `navigation_timeout_ms`: default 15000, maximum 60000 for page loading;
  a step's `timeout_ms` overrides it.

Failed results identify `launch_chromium`, `tls`, `navigation`, `auth`, `step`
or `validation`. TLS verification remains enabled.

`docker_logs` accepts `since` (RFC3339, within the last 24 hours), or the existing
`since_seconds` lookback. `component` filters a case-sensitive substring;
`level` selects ERROR, WARNING or INFO. The response contains a fixed `since` /
`until` window, `lines`, `bytes`, `truncated` and, when more records remain,
`next_cursor`. Pass that cursor with the same container and filters to continue.
Use `since` equal to the previous `until` to start a new window for later logs.

Each record retains at most 4096 bytes. A page retains at most 32768 log bytes
and 500 records; JSON escaping is bounded separately to fit the broker. A clipped
record sets `truncated: true`. Its remainder is deliberately discarded; the
cursor continues with the following record. Reading stdout and stderr uses
independent positions. A rotated log invalidates its cursor instead of silently
skipping records. Start a fresh window if that happens. Application traceback
text stays in log data and never becomes a tool error.

Release retention recognizes service-owned candidate binaries only when their
full hash, version, role and filename bind to a completed paired release.
Current, rollback, unfinished releases and pending build dependencies stay
protected. Unknown ownership or changed bytes preserve the whole version family.
