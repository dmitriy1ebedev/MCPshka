# P7: матрица изолированного восстановления

Все сценарии используют временные fixtures без живых сессий и изменений хоста.
Точный source — Git tree коммита с этой матрицей; результат и job ID находятся
в квитанции project_test. Старые production-проверки не принимают новый source.

| Сбой | Пакет / Go-тест | Durable ID и наблюдаемый результат без replay |
|---|---|---|
| Disconnect | workflow/TestPipelineSurvivesColdSessionAndNeverDuplicatesStages | workflow ID, stage job IDs; каждый этап один раз |
| Restart | workflow/TestLostExitAfterRestartCompensatesWithoutInventingSuccess | исходный workflow/job; компенсация без выдуманного exit |
| Timeout/lost reply | workflow/TestLostLaunchReceiptNeverReplaysOrUnlocks | исходный claim/launch; HOLD и writer fence |
| Повтор | deploy/TestStartReservedUsesDurableIdentityAndNeverOverwritesReceipt | закреплённый deployment ID; квитанция не переписывается |
| Незавершённая установка | workflow/TestFailedPipelineCompensatesAndFinalizesOnce | rollback/finalizer jobs; компенсация и завершение один раз |
| Откат controller+broker | releaseprep/TestNativeReliabilityMatrix/test_coordinated_upgrade.py | локальные квитанции и хеши парных образов; обе стороны восстановлены; повреждённый backup запрещает откат |
| Stale accepted/deployment_pending | deploy/TestReconcileSurvivesRestartWithoutReplayingOrClearingStaleAccepted | исходный deployment ID; UNKNOWN остаётся pending |
| Lease conflict | workflow/TestProfileDriftAndOtherWriterAreRejected | worktree writer claim; второй writer отклонён |
| Неполные metadata | rootbroker/TestCodexIncompleteMetadataKeepsKnownIDAcrossRestart | intent и IDENTIFIED/HELD; known-ID HOLD после cold read |
| Потерян ответ установки + cold tick | releasequeue/TestReleaseQueueLostApplyReplyColdTickNeverRepeatsInstall | исходный install, APPLYING; второй apply не вызывается |
| Потерян ответ job launch + restart | jobs/TestExternalLostStartDurableRestartNoReplay | исходный job binding; создание не повторяется |
| Restart lease | projectlease/TestTrackedLeaseHeartbeatSurvivesRestart | тот же holder/generation; чужой token не получает writer |
| Потерян ответ create | rootbroker/TestCodexUnknownReplyAndProbeFailureNeverReplayCreation | исходный intent и CLAIMED/HELD; UNKNOWN без заменяющего create |

Парный rollback проверяется настоящим helper на фейковых файлах и процессах,
через существующий Go wrapper в очереди project_test. Это не живая приёмка.
