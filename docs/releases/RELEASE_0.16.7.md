# MCPshka 0.16.7

Выпуск вместе с Lane 2.0.0 (19.11).

- Нативное чтение Lane: `laneprovider.Reader` ходит в read-only сокет Lane 2.0
  `/run/lane-read/api.sock` (группа mcpshka, лимит 4 МиБ / 45 с, только
  `lane.diag.export`, `lane.device.list`, `lane.device.show`). Инструмент `lane`:
  `device list` и новое `diag export` (`since`: 30m, 24h, 0) читаются напрямую,
  без root-брокера; нет сокета (Lane до 2.0) — прежний рецепт брокера. Изменения Lane
  по-прежнему только через брокер и Face ID.
- FreeLLM: файл `/etc/mcpshka/freellm-night.off` выключает ночной автозапуск
  (решение владельца 19.11 «качество, а не количество»); вызов по запросу работает.
