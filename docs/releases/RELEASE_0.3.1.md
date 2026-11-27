# MCPshka 0.3.1 — Lane снова проходит roadmap_run; CI делит работу между площадками

Дата: 2026-11-03. База: main 1c00bd7 (0.3.0). Боевой тест владельца: «продолжить роадмап Lane только через MCPshka».

## Исправлено
- **roadmap_run Lane (BUG-20261103-4):** sealed-путь (`roadmapscan`) принимал только `## P0 — Заголовок`, а Lane пишет `## P0: Заголовок`; `project_context` и тест-гейт падали «no roadmap phases» → каждый прогон Lane с 01.11 останавливался UNKNOWN через минуту (lane-20261101-155208-141d, lane-20261103-042422-8f5e). Теперь принимаются оба формата; неоднозначные (`## P0:X`, `## P0 X`, `### P0:`) по-прежнему отклоняются. TestParseColonHeadingsAsLaneWritesThem.
- **integration_publish_main (BUG-20261103-1):** неотслеживаемый файл на месте добавляемого кандидатом → раньше «fast-forward outcome UNKNOWN», теперь до merge `CHECKOUT_UNTRACKED_COLLISION` с путями (локальные файлы не трогаются); off-target/dirty HOLD типизированы (FAILED_VERIFIED, next git_status). TestPublishNamesUntrackedCollisionBeforeMerging, TestPublishOffTargetHoldIsTyped.

## CI fan-out (решение владельца 03.11: не повторять одно и то же, а делить)
- `packaging/ci/shard.py` + `plan.json`: LPT по измеренному весу пакета (GitVerse run 1997237) и скорости площадки, детерминированно; недоступная площадка — её доля остальным (`SHARD_SITES`/`shard_sites`).
- GitVerse snapshot-contracts — единственный полный шлюз на коммит (дубль `ci.yml` на push в main убран). MosHub — своя race-доля (без повторного `go test ./...` и ARM64-сборки). GitHub dispatch — своя race-доля, fuzz, ARM64-релиз; PR (GitHub как шлюз) — полная матрица. docs/CI_FANOUT.md.
- MosHub offline image: сборка от root на раннерах GitVerse (rootless buildah: `unshare(CLONE_NEWUSER)` запрещён, BUG-20261103-2).
- История GitHub main (d046f8b, 3 коммита 19.10 только на GitHub, уже содержащиеся в main) влита `-s ours` — дерево не меняется, GitHub main перематывается без force.

## Проверка
- go test: ./internal/roadmapscan, ./internal/integrationqueue (H96, project_test).
- python: packaging/ci, packaging/moshub, packaging/gitverse, packaging/cloud — OK.
- CI: GitVerse snapshot-contracts; GitHub race-доля 7b601e5 — success (run 41578036370).
