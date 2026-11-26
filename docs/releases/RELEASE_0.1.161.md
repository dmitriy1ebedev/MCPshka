# MCPshka 0.1.161

CI evidence gate fix on top of 0.1.160. Its first live run held correctly:
GitHub `main` (`d046f8b`) and the local integration lineage (`47924ee`) hold
the same tree but different commits (PR #66 was merged on GitHub separately),
so every PR base looks diverged and CI tests the commit merged with it.

## Fix

- **The gate proves the tested tree instead of requiring an ancestor base.**
  Root reads the PR base the runs merged with. An ancestor base is accepted as
  before. Otherwise root merges base and commit locally (`git merge-tree
  --write-tree` into a private object directory; the repository is not
  written) and requires a clean merge whose tree equals the commit's tree. A
  base that adds changes or conflicts holds with its reason; a base missing
  locally holds with "fetch the GitHub remote first". The GitHub compare call
  is no longer used.
- **Publishing closes the queue item.** The protected pipeline (approval, root
  worker, publish) never ran the queue's own executor, so integrated items
  stayed `pending` forever. `integration_publish_main` now marks the exact
  request and execution `integrated` once the target is at its commit;
  repeating publish retries the bookkeeping.

