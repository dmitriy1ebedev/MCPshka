# MCPshka 0.10.1 — 2026-11-09

Исправления сразу после 0.10.0, найденные живой проверкой.

- **code_run через tool_invoke.** В 0.10.0 инструмент регистрировался в обход `addTool`: прямой вызов работал, но `tool_invoke` отвечал «unknown tool», а `tool_resolve` показывал effect `gated_mutation`. Теперь он в реестре как read-only; регрессионный тест `TestCodeRunChainsReadOnlyCalls`.
- **Автоприёмка старых fixed-карточек.** 0.10.0 при старте приняла 38 карточек; ещё ~100 fixed-карточек не имели release. Теперь карточка принимается, если её commit — предок коммита `0.10.1:` на main (`git merge-base --is-ancestor`). Тест `TestShipReleaseAcceptsAncestorFixes`.
- **Code Mode без паник.** Паника вызванного инструмента превращается в ошибку сценария; аргументы проходят через JSON (числа — float64), как при обычном вызове. Тесты `internal/codemode`.
