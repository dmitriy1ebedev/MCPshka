#!/usr/bin/env bash
# MCPshka 0.1.103: one-time, independently authorized root-broker maintenance.
# Execute only from a root-owned *checksum-verified* copy.
set -Eeuo pipefail
umask 077
readonly EXPECTED_SHA256="2d8fdf811e9a9319287f5f963b7685c055e2f49c74c62fdca3b455c0e6594783"
readonly SOURCE="/root/repos/mcpshka/bin/mcpshka-broker-0.1.103.candidate"
readonly TARGET="/usr/local/libexec/mcpshka-broker"
readonly BROKER_UNIT="mcpshka-broker.service"
readonly AGENT_UNIT="mcpshka.service"
readonly RUNDIR="/var/lib/mcpshka-executor"

[[ "${EUID}" -eq 0 ]] || { echo "root authentication required" >&2; exit 2; }
[[ "${EXPECTED_SHA256}" =~ ^[0-9a-f]{64}$ ]] || { echo "unbuilt release: HOLD" >&2; exit 2; }
[[ -d "${RUNDIR}" && ! -L "${RUNDIR}" ]] || { echo "unsafe root state directory" >&2; exit 2; }
[[ -f "${SOURCE}" && ! -L "${SOURCE}" && -x "${SOURCE}" ]] || { echo "candidate missing or aliased" >&2; exit 2; }
[[ -f "${TARGET}" && ! -L "${TARGET}" && -x "${TARGET}" ]] || { echo "installed broker missing or aliased" >&2; exit 2; }
[[ "$(readlink -f -- "${SOURCE}")" == "${SOURCE}" ]] || { echo "source alias: HOLD" >&2; exit 2; }
[[ "$(readlink -f -- "${TARGET}")" == "${TARGET}" ]] || { echo "target alias: HOLD" >&2; exit 2; }

# Snapshot both binaries into a root-only directory before changing production.
work="$(mktemp -d "${RUNDIR}/broker-upgrade-0.1.103.XXXXXXXX")"
chmod 0700 "${work}"
committed=0
changed=0
recover() {
  local status=$?
  trap - EXIT
  if [[ "${committed}" == 0 && "${changed}" == 1 ]]; then
    echo "Health gate failed: restoring previous privileged broker" >&2
    install -o root -g root -m 0755 "${work}/previous" "${work}/rollback"
    mv -f -- "${work}/rollback" "${TARGET}"
    systemctl restart "${BROKER_UNIT}" || true
  fi
  [[ "${committed}" == 1 ]] && echo "Verified previous broker stored at ${work}/previous"
  if [[ "${committed}" == 0 ]]; then rm -rf -- "${work}"; fi
  exit "${status}"
}
trap recover EXIT
install -o root -g root -m 0600 -- "${TARGET}" "${work}/previous"
install -o root -g root -m 0755 -- "${SOURCE}" "${work}/candidate"
printf '%s  %s\n' "${EXPECTED_SHA256}" "${work}/candidate" | sha256sum --check --status || { echo "candidate SHA-256 mismatch: HOLD" >&2; exit 3; }
if cmp -s "${work}/candidate" "${TARGET}"; then
  echo "root broker already installed: unchanged"
  committed=1
  exit 0
fi
mv -f -- "${work}/candidate" "${TARGET}"
changed=1
systemctl restart "${BROKER_UNIT}"
for attempt in $(seq 1 20); do
  if systemctl is-active --quiet "${BROKER_UNIT}" &&
     test -S /run/mcpshka-broker/broker.sock &&
     systemctl is-active --quiet "${AGENT_UNIT}" &&
     curl --silent --fail --max-time 2 http://127.0.0.1:8765/healthz | grep -q '"version":"0.1.103"'; then
    [[ "$(sha256sum "${TARGET}" | cut -d' ' -f1)" == "${EXPECTED_SHA256}" ]] || exit 4
    committed=1
    echo "BROKER_0.1.103_INSTALLED VERIFIED; rollback retained"
    break
  fi
  sleep 1
done
if [[ "${committed}" != 1 ]]; then echo "broker upgrade health gate failed" >&2; exit 5; fi
