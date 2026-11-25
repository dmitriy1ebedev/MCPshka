# MCPshka 0.12.6 — дом впереди тяжёлого

## Зачем

09.11 Zigbee2MQTT умер под `go test` (BUG-20261109-1), 09.11 же H96 ушёл в своп (MCP-159). 0.12.4–0.12.5 убрали лишние запуски процессов; этот выпуск доделывает приоритет дома из ROADMAP P5/P8 и три пункта S1/S5.

## Два класса нагрузки (P5)

«Дом» — `docker.service`, `containerd.service` и scope контейнеров Zigbee2MQTT/MQTT/HA: CPUWeight/IOWeight 1000. «Тяжёлое» — sealed-тесты исполнителя, roadmap_run, agent_task и scope заданий: CPUWeight 20, IOWeight 10, Nice, IO idle; консоли ИИ (`user.slice`) — 30/30 и не больше 3 ядер. Веса работают только при борьбе за ресурс.

Отклонение от текста пункта: общий `mcpshka-heavy.slice` не сделан — cgroup исполнителя закреплён за `/system.slice/mcpshka-executor-*.service` по всему коду fence. Тот же эффект дают одинаковые свойства на каждом тяжёлом юните.

## Обратимо (P5)

Таблица весов — `internal/homeguard/priority.go`. Файл `/etc/mcpshka/home-priority-off` выключает guard и один раз ставит юнитам значения загрузки (`systemctl set-property --runtime unit CPUWeight= IOWeight= CPUQuota=`; это и есть бэкап: runtime-свойства существуют, только пока guard их ставит). `systemctl revert` не используется — он снёс бы и drop-in'ы из /etc. `project_verify` добавляет проверку `home_priority`: сравнивает `systemctl show` с таблицей (или со значениями загрузки, когда guard выключен) и называет каждое расхождение; на `ok` выпуска не влияет, как dns/outbound_https.

## Проба MQTT и моста Z2M (P8)

Раз в минуту homeguard публикует `ping` в `mcpshka/homeguard/echo` и ждёт, пока mosquitto доставит его обратно (`mosquitto_rr` подписывается раньше публикации) — это путь любой автоматизации дома. Запуск — через Docker Engine API (`dockerapi.Exec`), не CLI. Нет эха за 3 с или дольше 2 с — degraded, новая тяжёлая работа ждёт. У работающего Zigbee2MQTT без ошибок адаптера читается `zigbee2mqtt/bridge/state`; `offline` — degraded. `owner_portal`: девятый индикатор «Дом: Zigbee/MQTT», в detail — время эха или причина.

## Стадии выпуска (S1)

`release_apply_status.stages`: `implementation_complete` (выпуск поставлен в очередь), `integrated` (после SHIP), `production_verified` (DONE и работает именно эта версия), `accepted` — всегда false: приёмка бывает у пункта ROADMAP или карточки, у выпуска её нет. В `queue` — цепочка: commit, CI test permit, build permit и source digest кандидата, плюс установленная версия и версия отката.

## Ручные команды → карточка (S5)

`exec_start` считает команды (имя + первый аргумент без флагов, мост `mcpshka` не считается). 5+ запусков за сутки, замеченных на двух проходах сторожа подряд, дают одну карточку P2 «exec_facade …» — сделать нативный инструмент.

## Полнота каталога (S5)

`TestGuidesNameLiveTools`: каждый `snake_case` в обратных кавычках в `internal/agentguide/guide/*.md` и `skills/*/SKILL.md` — живой инструмент, его параметр или поле результата из короткого списка; заменённый инструмент в гайде — ошибка CI.

## MCP-164

`docker inspect` брокера с двумя разрешёнными шаблонами отвечает из `GET /containers/NAME/json` (`dockerapi.Inspect`), значения совпадают с CLI (живой тест).
