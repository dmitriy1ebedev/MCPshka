# MCPshka 0.9.9

## ИИ держит задачу сам (владелец 09.11: «не каждый чих мне докладывать и тормошить разработку»)
- `forge_ci provider=gitverse mode=wait path=/root/repos/<проект> commit=<sha>` — одно задание (до 30 мин) опрашивает CI проекта до PASS/FAIL, включая ещё не стартовавший прогон. Чат ждёт его обычным `job_wait`, как тесты, и не останавливается на разовом RUNNING. Ответы `publish`, `rerun` и `phase_gate` теперь ведут на `mode=wait`.
- Правила без противоречий: rules п. 16 — ожидание теста/CI не конец хода, ждать то же задание (без дублей, п. 10), параллельно — независимая работа, владельцу — итог блока или настоящий blocker; п. 14 — один порядок полного прогона: CI проекта → живой раннер → `project_test` на H96 (тот же в `runners` и `phase_gate`); `tests` — гейт фазы засчитывает sealed-тест, CI проекта или полный go-test раннера.

## Дом важнее тяжёлого (BUG-20261109-1: Zigbee2MQTT упал под go test)
- Брокер (`homeguard`) раз в 5 мин ставит runtime-веса: контейнеры дома (zigbee2mqtt, mosquitto, homeassistant, matter-server), docker и containerd — CPU/IO вес 1000; user.slice (консоли ИИ и людей) — вес 30 и потолок 3 ядра из 4. Sealed-тесты уже шли с весом 20, Nice 19, idle-IO. Ребут сбрасывает веса, брокер ставит их снова; выключатель — файл `/etc/mcpshka/home-priority-off`.
- Проба дома раз в минуту: Z2M/MQTT запущены, но unhealthy, или в логах Z2M за 2 мин ASH/«Adapter disconnected» → `home degraded`. Тяжёлая очередь не пускает новые задания (WAITING_RESOURCE, не FAIL) до восстановления; `host_triage` показывает проблему «zigbee/mqtt» с `docker_logs`. Остановленный руками контейнер не держит очередь; протухшая (>3 мин) метка не блокирует.
- rules п. 10: тяжёлое (go test/build/make) в своей консоли ИИ запрещено — только `project_test`, `runner_task`, `forge_ci`.

## Раннер вместо очереди H96
- `project_test` sealed-профиля, вставший в очередь H96, сразу называет свободный живой раннер (`runner_task … profile=go-test`); `phase_gate` засчитывает то, что пройдёт первым.

## Баги
- BUG-20261109-2: `infra:true` теперь и для «Unexpected HTTP response: 504» (setup-python на GitVerse).

## Откат
- Веса: `touch /etc/mcpshka/home-priority-off` и ребут (или `systemctl set-property --runtime user.slice CPUQuota= CPUWeight= IOWeight=`). Весь выпуск — `release_apply` предыдущей 0.9.8.
