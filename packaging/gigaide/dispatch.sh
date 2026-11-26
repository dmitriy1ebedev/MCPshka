#!/bin/bash
# H96 side of the GigaIDE lane, run as the service user with its GitVerse key.
#   dispatch.sh publish <source sha>  queue the GitVerse snapshot of <sha>
#                                     (forge_ci gitverse publish must be done)
#   dispatch.sh status  <source sha>  print the worker's summary.json, if any
set -euo pipefail
K=/var/lib/mcpshka/gitverse
R=git@gitverse.ru:dmitriy_1ebedev/mcpshka-ci.git
export GIT_SSH_COMMAND="ssh -F /dev/null -i $K/id_ed25519 -o IdentitiesOnly=yes -o BatchMode=yes -o UserKnownHostsFile=$K/known_hosts -o StrictHostKeyChecking=yes -o ConnectTimeout=8"
mode=${1:?publish|status}; sha=${2:?source sha}
[[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo "sha must be 40-hex"; exit 2; }
cd /root/repos/mcpshka
case $mode in
  publish)
    snap=$(git ls-remote "$R" "refs/heads/mcpshka-snapshots/$sha" | cut -f1)
    [ -n "$snap" ] || { echo "HOLD: publish the GitVerse snapshot first (forge_ci provider=gitverse mode=publish)"; exit 2; }
    git fetch -q "$R" "refs/heads/mcpshka-snapshots/$sha"
    git push -q "$R" "$snap:refs/heads/gigaide-jobs/$sha" && echo "{\"state\":\"QUEUED\",\"snapshot\":\"$snap\"}" ;;
  status)
    if git fetch -q "$R" "refs/heads/gigaide-results/$sha" 2>/dev/null; then
      git show FETCH_HEAD:summary.json
    else
      echo '{"state":"WAITING","note":"no result yet: the workspace must be open (worker auto-starts)"}'
    fi ;;
  *) echo "publish|status"; exit 2 ;;
esac
