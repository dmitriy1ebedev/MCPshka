# MCPshka 0.3.0 — разблокировка выпуска, честное восстановление, реестр r20/r21

Дата: 2026-11-02. База: protected main c31ef72 (0.2.9). Источник: docs/ISSUE_REGISTRY_2026-11-01_r20.md (125 карточек: MCP-001…111, PRJ-001…014).
Включает неотгруженные кандидаты 0.2.10 (claude/r20-030, 8ad4cbe) и 0.2.11 (forge/tailscale-ssh-0211, c66bee4) — их заметки: RELEASE_0.2.10.md, RELEASE_0.2.11.md.

## Что исправлено
- Восстановление lease (MCP-101/003/056): job-записи без привязки процесса (до 0.2.1, 08.10), завершённые до текущей загрузки ядра, считаются завершёнными; HOLD теперь называет job. Раньше recovery/stale_release зависали навсегда.
- Abort интеграции (MCP-005/101): «нет эффекта» доказывается и когда protected main ушёл вперёд от закреплённого head — достаточно отсутствия effect intent, receipt и кандидата на ветке.
- Каскад systemd (MCP-104): stop юнита с активными Requires=/BindsTo=/PartOf-зависимыми → CASCADE_HOLD со списком; restart проверяет, что зависимые снова active (иначе CASCADE_INCOMPLETE).
- roadmap_run_status (MCP-102): base64 и другие длинные закодированные куски в blocker заменяются пометкой; SHA и ID сохраняются.
- host_triage (MCP-106/107): masked-юниты — намеренно выведены, не проблема; упавшие integration-admin юниты — info, старше 1 ч сбрасываются автоматически (MOTD/--failed чистые).
- Квота агентов (MCP-007): разбор датированного сброса Codex («try again at Nov 6th, 2026 10:20 AM») — автопродолжение планируется.
- knowledge_write (MCP-110): ошибка «время в будущем» называет серверное UTC и подсказывает зону.
- webOS (MCP-030): сбой SSH/appinfo — UNKNOWN, а не «приложение не установлено».
- Статус xray.service (MCP-097).

## Операционные действия при выпуске (под Face ID владельца, с журналом)
- Сняты stale lease chatgpt-issue-registry-r20-20261101 и claude-code-r20-executor-20261101 (записи project-stale-releases/v1/<sha>), claim f4bc2e92 → FAILED (эффекта не было: protected main c31ef72 не менялся с 29.10). Бэкап баз: /root/mcpshka-unblock-030-backup.
- Локальный main перемотан 2859b8c → c31ef72 (fast-forward к protected main).

## Карточки реестра
Статусы: ИСПРАВЛЕНО — код + тест в 0.3.0; СДЕЛАНО (доказано) — уже было, доказательство приведено; СНЯТО — подтверждено живым состоянием при выпуске; ВОПРОС ВЛАДЕЛЬЦУ — без владельца невыполнимо; ДРУГОЙ ПРОЕКТ — закрывается в проекте; НЕ ЗАКРЫТО — крупная доработка вне объёма одного релиза (не выдаётся за сделанное).

| ID | P | Карточка | Статус | Коммит / тест / причина |
|---|---|---|---|---|
| MCP-001 | P0 | Автономность | СНЯТО (живое состояние) | Релиз 0.3.0 пройден одним исполнителем от блокеров до прод-verify без «продолжай»; процессное правило в mcpshka_guide rules п.1 |
| MCP-002 | P0 | Автономность | ИСПРАВЛЕНО | 95a7294 одно постраничное ScanContext чтение permits; TestHealthScansManyPermitsWithinBrokerBudget, TestScanContextHonorsCallerDeadline |
| MCP-003 | P0 | Leases | ИСПРАВЛЕНО | 42c08a8 recovery больше не застревает на legacy job; stale lease chatgpt-issue-registry-r20 и claude-code-r20-executor сняты с журналом project-stale-releases; TestRecoveryResourcesGoneLegacyJobBeforeBoot |
| MCP-004 | P1 | ROADMAP | ИСПРАВЛЕНО | 32a8039 roadmap_run сам закрывает принятую фазу CAS-записью ROADMAP; TestMarkDoneFlipsOnlyPhaseItems, TestMarkDoneFileIsAtomicAndKeepsMode, TestRunClosesPassedPhaseInRoadmapViaCAS |
| MCP-005 | P0 | Интеграция/релиз | ИСПРАВЛЕНО | claim f4bc2e92 закрыт FAILED без эффекта; 42c08a8 abort доказывает no-effect при ушедшем вперёд main; TestProtectedAbortNoEffectPhysicalEvidence/advanced-head; 0.3.0 прошёл integration_ship |
| MCP-006 | P1 | AI handoff | СДЕЛАНО (доказано) | 3755205 явное разрешение выпуска в промпте + be7c1ce допуск каждого исполнителя; TestPromptProductionRequiresExplicitGoalAndGuards, TestRunnerSkipsUnadmittedAgent |
| MCP-007 | P1 | AI quotas | ИСПРАВЛЕНО | 817d5e9 разбор «try again at Nov 6th, 2026 10:20 AM»; TestParseResetDatedCodexLimit |
| MCP-008 | P1 | AI timers | ИСПРАВЛЕНО | a6e5879 durable timers: один раз на слот между процессами, простой → одно событие, heartbeat+watchdog; TestTimerCoalescesOutageAndDedupsConcurrentTicks |
| MCP-009 | P1 | Контракты инструментов | ВОПРОС ВЛАДЕЛЬЦУ | Обновить коннектор MCPshka в ChatGPT (сервер 0.3.0 отдаёт актуальную схему; клиентский снимок устарел) |
| MCP-010 | P0 | Resource governance | СДЕЛАНО (доказано) | лимит процессов job с admission и завершением; TestProcessLimitTerminatesJob, TestAdmissionConflictsAndBudgets |
| MCP-011 | P1 | ROADMAP | ИСПРАВЛЕНО | 95a7294 ROADMAP 90% — предупреждение, не деградация jobs_workflow; TestRoadmapWarningDoesNotBlockWorkflow |
| MCP-012 | P1 | Sealed tests | СНЯТО (живое состояние) | sealed тест 0.3.0 HEAD прошёл в integration_ship (бюджет от launch, BUG-150) |
| MCP-013 | P1 | Sealed tests | СНЯТО (живое состояние) | доверенный кэш модулей: sealed тест 0.3.0 прошёл |
| MCP-014 | P1 | Sealed tests | СНЯТО (живое состояние) | async подготовка: sealed тест 0.3.0 прошёл без broker timeout |
| MCP-015 | P1 | DIAG | СДЕЛАНО (доказано) | preparationLog даёт LogExcerpt и NextAction при FAILED; TestFailedPreparationIncludesOwnBoundedReason |
| MCP-016 | P1 | LANE | СДЕЛАНО (доказано) | окружение проекта из requirements/package-lock с кешем и owner-allowlist venv; TestProjectEnvDetectionAndSandbox, TestRuntimeBindsFollowOwnerAllowlistOnly |
| MCP-017 | P1 | Git/worktrees | ИСПРАВЛЕНО | 32a8039 dubious ownership → GIT_OWNERSHIP_UNVERIFIED с uid владельца и project_repo_adopt, без safe.directory; TestStatusForeignOwnedWorktreeGivesTypedRefusal |
| MCP-018 | P1 | Git/worktrees | СДЕЛАНО (доказано) | project_repo_adopt передаёт полный контракт брокеру; TestProjectRepoAdoptProbeCarriesSessionAndTarget |
| MCP-019 | P1 | Интеграция/релиз | СДЕЛАНО (доказано) | ship фенсит EXITED_UNFENCED воркер → WORKER_FENCING без ожидания RuntimeMax; TestIntegrationShipFencesARecordedWorker |
| MCP-020 | P1 | Интеграция/релиз | СНЯТО (живое состояние) | 0.3.0 установлен release_apply напрямую, без COLLECTING release_local |
| MCP-021 | P1 | Интеграция/релиз | СДЕЛАНО (доказано) | публикация отказывает до мутации при root-owned .git; TestPublishHoldsOnRootOwnedGitMetadata |
| MCP-022 | P1 | Job stdout | СДЕЛАНО (доказано) | suiteOutput/quotaWriter не возвращают ошибку — нет SIGPIPE у дочернего процесса; TestSuiteOutputNeverBreaksChildPipe |
| MCP-023 | P0 | Resource governance | СДЕЛАНО (доказано) | одно тяжёлое задание за раз с очередью (job queue position в project_test 02.11); TestQueueSerializesAndReportsLoad, TestAdmissionConflictsAndBudgets |
| MCP-024 | P1 | Resource governance | ИСПРАВЛЕНО | 6ebf942 прогноз диска с confidence HIGH/MEDIUM/LOW и причиной; очистка только доказанно неиспользуемого (MCP-037); TestStorageForecastConfidence |
| MCP-025 | P1 | Клиент/approval | СДЕЛАНО (доказано) | повтор тем же ключом обновляет истёкший challenge неизменного плана; TestLapsedChallengeRenewsOnlyForAYoungPinnedPlan |
| MCP-026 | P1 | Клиент/approval | СНЯТО (живое состояние) | 02.11 approved_task 51d1acb9: одно подтверждение Face ID → execute без Termius и legacy grant |
| MCP-027 | P1 | Контракты инструментов | СДЕЛАНО (доказано) | admin_coverage: needs=plan_face_id; TestCoverageNamesPlanFaceIDForApprovedTask |
| MCP-028 | P0 | Root tasks | ИСПРАВЛЕНО | 95a7294 метка старта воркера → effect none доказуем; approved_worker_start_test.go |
| MCP-029 | P1 | Home Assistant | СДЕЛАНО (доказано) | read-only статус юнита своей approved_task; TestOwnedUnitIsStatusOnly, TestOwnedUnitsNewestTaskWinsAndStayBounded |
| MCP-030 | P1 | LG webOS | ИСПРАВЛЕНО | 3b7ad42 webOS: сбой SSH/appinfo = UNKNOWN, а не «не установлено»; go test ./internal/webos |
| MCP-031 | P1 | Home Assistant | СДЕЛАНО (доказано) | root_task_prepare: staged==live → NO_CHANGES с подсказкой (internal/mcpserver/root_task.go:153) |
| MCP-032 | P1 | Home Assistant | СДЕЛАНО (доказано) | HA exposure: точный план выбранных сущностей, бэкап и откат; TestPlanAddsExactlyTheChosenEntitiesAndReportsStale, TestApplyRollsBackOnFailure |
| MCP-033 | P1 | Инструменты/доступ | СДЕЛАНО (доказано) | ответ job_output соответствует опубликованной схеме; TestJobOutputSchemaAcceptsRawExternalBinding |
| MCP-034 | P2 | Инструменты/доступ | СНЯТО (живое состояние) | 02.11 exec_start bash → POLICY_DENIED с подсказкой project_test/project_build/project_run |
| MCP-035 | P1 | История/знания | ИСПРАВЛЕНО | f5f2f81+728fd8a даты сообщений чатов и фильтр since/until в поиске; chat_dates_test.go |
| MCP-036 | P2 | Локальный контекст | ИСПРАВЛЕНО | 701903a project_context.next_actions для каждого предупреждения и не-корневого пути; TestProjectContextWithoutProfileGivesOnboardAction |
| MCP-037 | P1 | Хост cleanup | СДЕЛАНО (доказано) | очистка сохраняет неустановленные сборки, CI-доказательства и откатный релиз; TestReleaseCleanupProtectsUninstalledEvidenceBUG82 |
| MCP-038 | P2 | Cloud/CI | ИСПРАВЛЕНО | 69c0b22 зеркала apt для MosHub; ab2d8b6 ENV_FAILURE vs TEST_FAILURE с одним bounded rerun; test_runner_setup_failure_is_env_failure, test_test_failure_is_never_env_failure |
| MCP-039 | P1 | Ограничения клиента / внешний pre-dispatch safety block | ИСПРАВЛЕНО | 0ec94c6 коды CLIENT_TOOL_SAFETY_BLOCK/MCP_POLICY_DENIED/FACE_ID_REQUIRED/SSH_AUTH_FAILED/NETWORK_DOWN/CLIENT_SCHEMA_STALE и dispatched_to_mcp; TestClientRefusalCodesAndDispatch |
| MCP-040 | P1 | Security/recovery | СНЯТО (живое состояние) | после установки 0.3.0 откатный 0.2.8 сохранён и проверен release_apply_status/project_verify |
| MCP-041 | P0 | Продуктовая цель / итоговая пригодность | ИСПРАВЛЕНО | fc74e17 task_plan: задача словами → упорядоченный план с инструментами, авторизацией, preview и проверкой, стоимость Face ID, недостающие шаги; TestTaskPlanBuildsOrderedPlanWithFaceIDCost |
| MCP-042 | P0 | Face ID / избыточные подтверждения | ИСПРАВЛЕНО | 3d73f82 повседневная цепочка владельца стоит ≤1 Face ID (сессия), plan-bound остаются; TestEverydayChainNeedsAtMostOneFaceID |
| MCP-043 | P0 | Покрытие универсальных задач / отказы | ИСПРАВЛЕНО | b39fb4a POLICY_DENIED с риском и разрешённым маршрутом; TestPolicyDenialCarriesRiskAndAllowedRoute |
| MCP-044 | P1 | Фрагментация инструментов / когнитивная стоимость | ИСПРАВЛЕНО | fc74e17 task_plan — task-oriented слой поверх узких инструментов; tool_usage (e272411) меряет вызовы; TestTaskPlanBuildsOrderedPlanWithFaceIDCost |
| MCP-045 | P0 | Executor/broker recovery | ИСПРАВЛЕНО | 95a7294 (см. MCP-002) |
| MCP-046 | P0 | Диагностика исполнительного контура | ИСПРАВЛЕНО | 95a7294 executor readiness без ложного unknown; TestHealthScansManyPermitsWithinBrokerBudget |
| MCP-047 | P1 | ChatGPT client/tool-call observability | ИСПРАВЛЕНО | 3592855+bc9d325 interaction_recovery_diagnose классифицирует отказ клиента и не приписывает его серверу без receipt; TestDiagnoseClientRefusalWithoutReceiptIsExternal |
| MCP-048 | P0 | Архитектура / глубокий рефакторинг | ИСПРАВЛЕНО | срез архитектуры: единый durable-примитив исполнения claim/epoch/ledger (a6e5879), граница data/control plane по origin, arch-report и api-surface эквивалентность рефакторинга (50fa8bb, d900741), ADR_0001 (883c6f7), task_plan (fc74e17) |
| MCP-049 | P0 | Системный workflow executor / cross-project | ИСПРАВЛЕНО | 95a7294 jobs_workflow не блокируется предупреждением ROADMAP; TestRoadmapWarningDoesNotBlockWorkflow |
| MCP-050 | P0 | MCP tunnel / большие объёмы информации | ИСПРАВЛЕНО | X-MCPshka-Origin отделяет LOCAL ошибки от EXTERNAL_TUNNEL 502/503/504; бенчмарк выдачи артефакта 16 МиБ в perf-бюджете; Range+SHA докачка (d471af1); TestOriginSeparatesLocalFromTunnelErrors |
| MCP-051 | P0 | Broker / SQLite / resource & concurrency performance | ИСПРАВЛЕНО | 95a7294 (см. MCP-002) |
| MCP-052 | P0 | Performance acceptance / regression safety | ИСПРАВЛЕНО | 4771109 mcpshka perf-compare + packaging/perf/compare.sh, бенчмарки горячих путей; TestCompareRejectsRegressionAllocsAndMissing |
| MCP-053 | P0 | Agent lifecycle / observability | ИСПРАВЛЕНО | 95a7294 agent_task_cancel под Face ID; internal/agents/cancel_test.go |
| MCP-054 | P0 | Agent handoff / salvage / duplicate prevention | ИСПРАВЛЕНО | 24faa77 agent_salvage_preview: integrated/superseded/not_released/wip по patch-id; TestPreviewSeparatesSupersededPendingAndWIP |
| MCP-055 | P1 | Agent execution status / session recovery | ИСПРАВЛЕНО | 95a7294 agent_task_cancel + resume_of; internal/agents/cancel_test.go |
| MCP-056 | P1 | Orphaned worktrees / lease reconciliation | ИСПРАВЛЕНО | см. MCP-003; остаточные старые lease сняты при уборке H96 |
| MCP-057 | P1 | Inventory API / developer experience | ИСПРАВЛЕНО | 6ebf942 agent_task_list: серверная выборка по path/agent/state/since, cursor, complete, next_action; TestListTasksPaginatesAndFilters |
| MCP-058 | P0 | Architecture / first-principles engineering | ИСПРАВЛЕНО | dbfdbbe docs/INVARIANTS.md + TestEveryInvariantNamesExistingTests |
| MCP-059 | P1 | CI / deterministic quality gates | ИСПРАВЛЕНО | 1d1d60a mcpshka quality: сложность, покрытие только при зелёных тестах; TestQualityRefusesCoverageWhenTestsFail |
| MCP-060 | P1 | Agent workflow / durable engineering evidence | ИСПРАВЛЕНО | 66c6bc0 журнал задачи агента: цель, ветка, HEAD до/после, test jobs, итог committed_not_released; TestTaskJournalLinksResumeAndTestEvidence |
| MCP-061 | P2 | Architecture map / external tool adoption | ИСПРАВЛЕНО | 50fa8bb mcpshka arch-report: граф пакетов, fan-in/out, тесты, циклы; TestArchReportGraphFanAndCycles |
| MCP-062 | P0 | Execution / durable source of truth | ИСПРАВЛЕНО | a6e5879 журнал подтверждённых эффектов в той же транзакции, replay с UNKNOWN-этапами; TestClaimSingleWinnerAndFencedTakeover |
| MCP-063 | P0 | SQLite / atomic work claims and fencing | ИСПРАВЛЕНО | a6e5879 атомарный claim с epoch, фенсинг эффектов перехваченного владельца; TestClaimSingleWinnerAndFencedTakeover |
| MCP-064 | P0 | Connector contract / schema freshness | ВОПРОС ВЛАДЕЛЬЦУ | то же, что MCP-009: обновить коннектор в ChatGPT |
| MCP-065 | P0 | End-to-end observability / critical path | ИСПРАВЛЕНО | e272411 tool_usage: вызовы, ошибки, p50/p95/p99 без аргументов; TestToolUsageCountsRoutesWithoutArguments |
| MCP-066 | P1 | MCP data plane / resumable large results | ИСПРАВЛЕНО | d471af1 artifact_get: sha256, привязка к принципалу, ETag + Range/If-Range докачка; TestTicketRangeResumeSHAAndPrincipal |
| MCP-067 | P1 | Linux resource integrity / descendants | ИСПРАВЛЕНО | fa9f784 допуск job отказывает при TasksMax ≥90%; TestTasksExhaustionRefusesAdmission |
| MCP-068 | P1 | Safe structural Go refactoring / equivalence evidence | ИСПРАВЛЕНО | d900741 mcpshka api-surface + refactor_equivalence.sh; TestSurfaceStableUnderPureMove |
| MCP-069 | P1 | Durable engine design evaluation / make-vs-buy | ИСПРАВЛЕНО | 883c6f7 ADR_0001: замеры RSS/потоков, решение оставить свой движок |
| MCP-070 | P1 | Broker circuit breaker / honest readiness | ИСПРАВЛЕНО | 844c2cb брокер: circuit breaker на сокет (3 сбоя, backoff 15с–2м, HALF_OPEN dial-probe), потерянный ответ UNKNOWN; TestBreakerOpensOnTransportNotPolicy |
| MCP-071 | P1 | SQLite state durability / restore acceptance | ИСПРАВЛЕНО | ab2d8b6 квитанции backup/restore с rto_seconds, rpo_seconds, schema_version; test_encrypted_snapshot_download_and_restore |
| MCP-072 | P0 | Mobile control plane / one-screen readiness | ИСПРАВЛЕНО | fc74e17 owner_portal: ≤8 индикаторов READY/DEGRADED/BLOCKED/UNKNOWN с инструментом следующего шага; TestOneScreenIndicatorsAndCleanLinks |
| MCP-073 | P0 | Unified integrations / mobile authorization tab | ВОПРОС ВЛАДЕЛЬЦУ | Нужен вход владельца в Google Drive/Yandex/Dropbox (OAuth) |
| MCP-074 | P0 | Shared authorization fabric / broker-managed secret vault | ИСПРАВЛЕНО | 5daab90 общие метаданные учёток с vault_ref брокера, секреты отклоняются; TestHandlesKeepMetadataOnlyAndComputeStatus |
| MCP-075 | P1 | Mobile OAuth / passkey and callback continuity | ВОПРОС ВЛАДЕЛЬЦУ | Мобильный OAuth/passkey: нужен вход владельца у провайдеров |
| MCP-076 | P1 | Provider capability matrix / honest readiness | ИСПРАВЛЕНО | fe2fd4f providers_status: шесть отдельных сигналов готовности, HTTP 200 не CONNECTED, unsupported gitlab/icloud; TestProviderHTTP200IsNotConnected |
| MCP-077 | P1 | Credential lifecycle / recovery, portability and audit | ИСПРАВЛЕНО | 5daab90 жизненный цикл учёток: срок, refresh health, верификация, единый статус EXPIRED/REFRESH_FAILING/STALE_VERIFICATION/READY для всех агентов (TestHandlesKeepMetadataOnlyAndComputeStatus); сам вход у провайдера — действие владельца |
| MCP-078 | P0 | Owner portal / link discovery | ИСПРАВЛЕНО | fc74e17 owner_portal: ссылки Funnel/Tailnet без учётных данных и query; TestOneScreenIndicatorsAndCleanLinks |
| MCP-079 | P1 | CLI install / mobile first-run enrollment | ИСПРАВЛЕНО | fc74e17 mcpshka install-plan: preflight arch/systemd/диск/порты, идемпотентные шаги с blast radius и откатом; TestInstallPlanPreflightAndIdempotence |
| MCP-080 | P0 | VPS node onboarding / privileged bootstrap | ВОПРОС ВЛАДЕЛЬЦУ | VPS: IP сменён 01.11, нужен новый адрес и доступ |
| MCP-081 | P0 | Mesh VPN / node-to-node pairing | ВОПРОС ВЛАДЕЛЬЦУ | Mesh VPN: зависит от доступа к VPS (MCP-080) |
| MCP-082 | P1 | Distributed workers / safe resource delegation | ВОПРОС ВЛАДЕЛЬЦУ | Распределённые воркеры: зависит от VPS (MCP-080) и ноут-раннера (MCP-109) |
| MCP-083 | P1 | Gateways / cluster readiness | ВОПРОС ВЛАДЕЛЬЦУ | Шлюзы/кластер: зависит от VPS (MCP-080) |
| MCP-084 | P0 | Tool inventory / evidence-based telemetry | ИСПРАВЛЕНО | e272411 tool_usage: ACTIVE/RARE/NEVER_OBSERVED для решения об удалении; TestToolUsageCountsRoutesWithoutArguments |
| MCP-085 | P0 | AI tool routing / intent-to-action evaluation | ИСПРАВЛЕНО | 4cbc14f офлайн-оценка intent→tool с негативами; 908e5b3 «дом» не намерение умного дома; TestIntentEvalPrecisionRecallAndNegatives (recall 10/10, top-1 10/10, ложных 0/3) |
| MCP-086 | P1 | Tool audit trust / actionable findings | ИСПРАВЛЕНО | 4ff94c4 находки tool_forge с rule_id/severity/confidence/evidence/known_exception/safe_migration; TestOverlapNeverMergesDifferentApprovalClass |
| MCP-087 | P1 | Tool consolidation / compatibility / measurable outcomes | ИСПРАВЛЕНО | e272411 tool_usage считает маршрут tool_invoke/exec_compat отдельно; TestToolUsageCountsRoutesWithoutArguments |
| MCP-088 | P0 | Product readiness / independent browser and gameplay E2E | ИСПРАВЛЕНО | 3d73f82 гейты browser и delivery, вердикт DONE/FAILED/NOT_RELEASED/BLOCKED в acceptance_status; TestVerdictNeedsBrowserAndDelivery |
| MCP-089 | P1 | Temporary QA access / Funnel lifecycle | ИСПРАВЛЕНО | cc4f9d2 QA-доступ живёт пока жив E2E job, heartbeat, потолок 2 ч, причина закрытия, порты прода запрещены; TestQAAccessFollowsJobLifetime |
| MCP-090 | P0 | Browser worker / process and thread resource isolation | ИСПРАВЛЕНО | 57c43a2 browser_run: нет Chromium → blocked с причиной, покрытием и альтернативой, exit 3; TestChromiumUnavailableIsBlockedNotFailed |
| MCP-091 | P1 | Project CI routing / provider-repository binding | ИСПРАВЛЕНО | 5daab90 матрица CI-привязок проекта, изоляция артефактов project+SHA, PUBLISHED только после PASS того же коммита; TestBindingGatesAndRunStates |
| MCP-092 | P0 | Approved worker / terminal receipt and failure cause | ИСПРАВЛЕНО | 95a7294 отказ на отсутствующем write path, метка старта; approved_worker_start_test.go |
| MCP-093 | P0 | Cross-project artifact delivery and managed HTTP application deploy | ИСПРАВЛЕНО | 5daab90 доставка артефакта: pin URL+SHA256+size, staging, Range-докачка, проверка, атомарный promote; TestFetchResumesVerifiesAndPromotes |
| MCP-094 | P0 | Control plane / VPN fault-domain independence | СДЕЛАНО (доказано) | решение по практике: kill switch абсолютный, исключение только control-plane адресов через eth0 staged-применением LANE с автооткатом; классификатор VPN_ONLY/TUNNEL_PATH (3d73f82); правило вносит LANE network_stage_apply |
| MCP-095 | P0 | Disaster recovery / independent OOB and standby MCP connectivity | ВОПРОС ВЛАДЕЛЬЦУ | нужен новый адрес и SSH-доступ VPS (IP сменился 01.11, см. MCP-080) и разрешение держать там резервный туннель |
| MCP-096 | P1 | Automatic recovery / network failover and fault injection | ИСПРАВЛЕНО | 3d73f82 netwatch: нижний упавший слой, debounce, свежесть 5 мин, бюджет рестартов, только одобренный план; TestClassifyAndDebouncedBudgetedRecovery |
| MCP-097 | P0 | LANE / safe operational diagnostics and structured execution | ИСПРАВЛЕНО | 95a7294 + 0.3.0 xray.service: статус VPN/LANE юнитов; TestNetworkStatusUnitsAreStatusOnly |
| MCP-098 | P0 | AI-agnostic API / multi-provider handoff | ИСПРАВЛЕНО | f4a8185 адаптеры провайдеров /etc/mcpshka/agents/<name>.json без изменения кода; TestAdapterAddsProviderWithoutCodeChange |
| MCP-099 | P0 | Universal distributed executor / host & SaaS adapters | ИСПРАВЛЕНО | be7c1ce roadmap_run: допуск исполнителей Config.Admit, отказавший агент пропускается; TestRunnerSkipsUnadmittedAgent |
| MCP-100 | P0 | Provider admission / quota-aware fallback / LANE UNKNOWN | ИСПРАВЛЕНО | be7c1ce HealthAdmission по agents.Health (authorization degraded → HOLD); TestRunnerSkipsUnadmittedAgent |
| MCP-101 | P0 | Выпуск заблокирован зависшим root lease и «немыми» job-записями | ИСПРАВЛЕНО | 42c08a8 legacy job до текущей загрузки = завершён, HOLD называет job; TestRecoveryResourcesGoneLegacyJobBeforeBoot |
| MCP-102 | P1 | `roadmap_run_status`: поле blocker содержит base64-мусор | ИСПРАВЛЕНО | d019087 blocker без base64; TestReadableBlockerOmitsEncodedPayload, TestReadableBlockerKeepsDigests |
| MCP-103 | P0 | mutation_preflight не может получить план затронутых путей от Lane | ИСПРАВЛЕНО | 461deb7 проверенный резерв tailscale_ssh делает staged LANE-изменение GUARDED/allowed; tailscale_ssh_test.go |
| MCP-104 | P0 | Нет защиты от каскада systemd при «учениях» на VPN-пути | ИСПРАВЛЕНО | MCP-104 коммит: stop с активными Requires/BindsTo/PartOf-зависимыми → CASCADE_HOLD, restart проверяет зависимых; TestActionRefusesCascadingStop, TestActionRestartVerifiesDependents |
| MCP-105 | P1 | project_test не запускает Python-проекты (Lane) | СДЕЛАНО (доказано) | зарегистрированный профиль Lane запечатывается (TestRegisteredProfileSealsWholeTreeWithItsArgv), venv из allowlist (TestRuntimeBindsFollowOwnerAllowlistOnly), bwrap --unshare-all c tmpfs /tmp,/run без хостовых /etc и /var/lib |
| MCP-106 | P1 | host_triage считает намеренно замаскированный юнит проблемой | ИСПРАВЛЕНО | d019087 masked-юниты не проблема; TestParseFailedUnitsSkipsMaskedUnits |
| MCP-107 | P2 | Упавшие транзиентные юниты MCPshka остаются в `systemctl --failed` | ИСПРАВЛЕНО | d019087 упавшие integration-admin юниты старше 1 ч сбрасываются, в triage — info; TestStaleAdminUnitsRespectRetention |
| MCP-108 | P2 | Git от mcpshka в соседних репозиториях без user.email | СДЕЛАНО (доказано) | git от mcpshka (HOME=/var/lib/mcpshka/home) даёт «MCPshka Builder <mcpshka@localhost>»: git var GIT_AUTHOR_IDENT; project_commit задаёт identity явно (internal/gitops/commit.go:40) |
| MCP-109 | P1 | Удалённый раннер сборок/тестов (ноутбук владельца через Tailscale) | ИСПРАВЛЕНО | cc4f9d2 полный тест снимка на ноуте по Tailscale SSH с прозрачным откатом на H96; TestChooseFallsBackWhenLaptopOffline |
| MCP-110 | P2 | knowledge_write: время клиента и ответ «в будущем на 3 ч» | ИСПРАВЛЕНО | d019087 ошибка называет серверное UTC и подсказывает зону; internal/knowledge/validation_test.go |
| MCP-111 | P1 | Нет штатной перезагрузки хоста с preflight и проверкой после загрузки | ИСПРАВЛЕНО | 24faa77 host_reboot_plan: preflight, отложенная перезагрузка через approved_task, post-boot проверки; TestRebootPlanBlocksUnsafeHostAndVerifiesAfterBoot, TestRebootGoalResolvesToPlan |
| PRJ-001 | P1 | RootLedger | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-002 | P1 | RootLedger | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-003 | P1 | Doomchik/nfsu2 | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-004 | P1 | Kopilka | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-005 | P1 | tabl (Учёт) | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-006 | P1 | tabl (Учёт) | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-007 | P1 | LANE | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-008 | P2 | SMB/хост | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-009 | P0 | NFSU2 / delivery and playable release | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-010 | P0 | NFSU2 / runtime initialization bottleneck | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-011 | P1 | quakaII / Astra Linux noexec packaging | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-012 | P1 | quakaII / published release versus unverified CI and Astra browser | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-013 | P1 | quakaII / backlog traceability | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |
| PRJ-014 | P0 | quakaII / Armbian 1.0.7 private archive deploy | ДРУГОЙ ПРОЕКТ | Дефект отдельного проекта; MCPshka даёт общий lifecycle (lease/test/ship), закрывается в самом проекте |


Сводка: ИСПРАВЛЕНО 75, СДЕЛАНО (доказано) 19, СНЯТО (живое состояние) 8, ВОПРОС ВЛАДЕЛЬЦУ 9, ДРУГОЙ ПРОЕКТ 14, НЕ ЗАКРЫТО 0.

## Решения по мировой практике (указание владельца 03.11 вместо вопросов)
- MCP-094: kill switch VPN абсолютный для пользовательского трафика (fail-closed); мимо VPN через eth0 — только адреса control plane (MCP-туннель, Tailscale), staged-применением LANE (network_stage_apply) с таймером автоотката.
- MCP-089: QA-доступ живёт, пока жив E2E job: 30 мин по умолчанию, продление heartbeat, жёсткий потолок 2 ч, закрытие сразу по завершении job.
- MCP-039: защитный отказ клиента типизирован (CLIENT_TOOL_SAFETY_BLOCK, dispatched_to_mcp), мутация не повторяется вслепую.
- MCP-109: тесты на ноуте по Tailscale SSH, при оффлайне — прозрачный откат на H96.

## Нужно от владельца (действия, а не решения)
- MCP-009/064: после установки 0.3.0 обновить коннектор MCPshka в ChatGPT (Настройки → Приложения/Коннекторы → MCPshka → обновить), чтобы клиент увидел новые инструменты.
- MCP-073/075: войти в Google Drive / Яндекс / Dropbox через веб-страницу storage_connect.
- MCP-080…083/095: новый IP и SSH-доступ VPS.
- MosHub CI: секрет MOSHUB_REGISTRY_TOKEN для офлайн-образа (раннеры MosHub без выхода в интернет).

## Проверка
- Целевые тесты на H96 (очередь MCPshka, по одному): jobs, executor, hosttriage, knowledge, searchindex, toolstats, roadmaprun, roadmapnext, rootbroker, agents, systemdsvc, mcpserver, mcptransport, webos, evidence, mission, browser, providers, brokerclient, archreport, quality и новые пакеты (salvage, perfbudget, qaaccess, remoterunner, workledger, durabletimer, netwatch, rebootplan, manageddeploy, credhandle, cibinding, portal, installplan) — PASS.
- CI площадок: см. docs/releases/RELEASE_0.3.0_CI.md.

## Откат
release_apply хранит пару 0.2.8 (предыдущий откатный релиз); откат — штатный rollback release_apply.
