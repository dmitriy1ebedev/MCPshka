# MCPshka 0.1.192

Autopilot, этапы 5 и 8; находка host_diagnose.

- **webos_deploy** (этап 5): установка IPK проекта на LG через его собственный `webos/install.py` одним `approved_task` (Face ID на интерпретатор, inline-код, установщик, appinfo и IPK по SHA-256); `webos_deploy_status` подтверждает id и версию, прочитанные с самого ТВ, и пишет gate device.
- **approved_task**: закрепление входных файлов (`inputs`) по SHA-256 рядом с исполняемым — интерпретатор не выполнит подменённый скрипт; страница Face ID показывает их до команды.
- **media_stream** (этап 8): потоки go2rtc без перезапуска — list (пароли замаскированы), measure (FPS, кбит/с, задержка первого кадра), apply ограниченного ffmpeg-профиля к одному производному потоку, delete, rollback именно этого потока. Исходные потоки камер не меняются. Пакет `internal/mediapipe`.
- **host_diagnose**: отказы fork по лимиту pids из журнала ядра дают вывод с именем юнита. На живом хосте 25.10 нашёл 3 отказа в `mcpshka-broker.service` (TasksMax=32) — packaging: TasksMax=96.
