# Доступ к CI-площадкам

Аудит 17.11.2026 (UTC), база 0.16.0. Наблюдения API — UNTRUSTED, не разрешение на выпуск.

| Площадка | Проверено через git_provider_query | Состояние CI | Что требуется |
|---|---|---|---|
| GitHub | capabilities: credential_present; whoami: dmitriy1ebedev; ci_runs доступны | Последний run 44105829279 успешен (temporary pull runner); это не доказательство release gate | Исправить PyYAML в cloud.yml; перед назначением проверить Actions/workflow permissions и бесплатную квоту. providers_status возвращает квоту UNKNOWN: не считать её остатком 2000 минут |
| GitVerse | capabilities: credential_present; whoami: dmitriy_1ebedev; ci_runs доступны | Run 2135059 снимка 474d1c8 — waiting; предыдущий 2128119 cancelled | API доступен, исполнение сейчас не доказано; ограничивать ожидание, перераспределять шард при инфраструктурном отказе |
| MosHub (GitLab API) | capabilities: credential_present; whoami: lda6994; ci_runs доступны | Pipeline 95602, 95595, 95586 от 12.11.2026 — failed | Проверить jobs/logs; runner tag appsechub; образ MOSHUB_CI_IMAGE и доступ к зависимостям. Общий failed пока нельзя считать ошибкой кода |
| SourceCraft | Зеркало, в распределение CI не включается | CI не проверялся | Ничего для этого выпуска |

Новых токенов для API не требуется. Право dispatch/push и доступность раннера не выводятся из whoami. Отдельных CI-секретов для запуска go vet/go test снимка не нужно; production-секреты на площадки не передаются. Если бесплатная квота GitHub недоступна для чтения или исчерпана, площадка должна пропускаться, без платного использования. Реальные новые CI-запуски и публикация 0.16.1 этим аудитом не подтверждены.

Уточнение jobs API: MosHub pipeline 95602 / job 383013 — `stuck_or_timeout_failure`, `runner: null`, tag `appsechub`. Это отказ назначения раннера, а не падение тестов. Исправление CI не может создать shared runner: владелец MosHub должен обеспечить доступный runner с этим tag; до этого его шард перераспределяется. Для запуска без интернет-доступа нужен существующий offline-образ `MOSHUB_CI_IMAGE` с актуальным go.sum, Python/PyYAML, Git >=2.44 и tini.

Проверка GitHub permissions/billing: Actions `enabled=true`, `allowed_actions=all`; `/users/dmitriy1ebedev/settings/billing/actions` и `/settings/billing/usage` — HTTP 403. Репозиторий private. Нужен доступ токена к чтению billing/остатка бесплатных минут; без него GitHub пропускается при назначении новых шардов, уже существующее доказательство читается независимо. Это внешний prerequisite, не причина запускать полный module-тест.

Адаптер поддерживает legacy Actions billing и актуальный `usage/summary` за UTC-месяц. Для personal free/pro применяется консервативный лимит 2000 Linux-минут с множителями Windows/macOS и резервом на уже назначенные jobs; неизвестный SKU или отсутствие данных закрывают новое назначение. Для нового API требуется [Plan: read](https://docs.github.com/en/rest/billing/usage); одного доступа к репозиторию недостаточно.

Дополнительная живая проба MosHub: назначено 8 runners, ни одного online (offline/never_contacted). `/runners/6324` доступен и подтверждает tag `appsechub`; список `/projects/87912/runners` не возвращает tags. Адаптер читает tags через детали не более трёх online-кандидатов. Для восстановления площадки нужен живой runner, а не новый API-токен.
