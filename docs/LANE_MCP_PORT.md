# MCP-порт через Lane — контракт API

MCPshka на той же машине, что Lane, получает публичный адрес для ChatGPT/Claude без ручных туннелей. Код MCPshka — `internal/lanelink`, карточка «MCP-порт через Lane» на «Подключениях». Сторона Lane — раздел «MCP-порты» веб-панели.

## Принципы
- **Только исходящие соединения.** Коннектор Lane сам звонит наружу (как Newt у Pangolin): входящих портов на машине нет.
- **Туннель не заменяет авторизацию.** OAuth/bearer остаётся за MCPshka; Lane только доставляет трафик на `127.0.0.1:8765`.
- **Ничего не открыто без владельца.** Запрос на подключение ничего не публикует, пока владелец не подтвердит тот же код в панели Lane.
- **У каждого порта свой ключ.** Он даёт право только на свой порт: не на REST-токен Lane и не на MCP-токен Lane.

## Транспорт
Loopback REST Lane `127.0.0.1:18190`, конверт `lane.api/v1` (`docs/API.md` Lane): `POST /api/v1` {apiVersion, requestId, operation, params} → {ok, result{ok, …}, error{message}}. `GET /healthz` 200 — Lane есть. Ошибка операции: `result.ok=false, result.error="<по-человечески>"`. Неверный или отозванный ключ порта — HTTP 401/403.

## Операции
| Операция | Ключ | params → result |
|---|---|---|
| `mcpport.pair` | нет (класс `pair`: только loopback, лимит частоты, без эффекта до подтверждения) | `name, local_url, secret_sha256` → `request_id, code` (6 цифр), `expires_at` (≤10 мин) |
| `mcpport.pair_status` | нет; `secret` должен совпасть с `secret_sha256` | `request_id, secret` → `state` = pending / approved / denied / expired; при approved — один раз `port_id, token, public_url` |
| `mcpport.status` | Bearer ключ порта | `port_id` → `state` = online / offline / paused, `public_url, route` (`vps`/`funnel`/`openai`), `route_label` («через ваш сервер в NL»), `bytes_in, bytes_out, last_seen` |
| `mcpport.rotate` | Bearer | `port_id` → `token` (старый ключ недействителен сразу) |
| `mcpport.remove` | Bearer | `port_id` → {} (порт снят, ключ отозван) |

## Сторона Lane
- Раздел «MCP-порты»: имя, локальный адрес, путь наружу (`route_label`), Online/Offline, трафик; кнопки «Подтвердить код», «Пауза», «Удалить», «Новый ключ».
- Путь наружу — по приоритету с автопереключением: свой VPS с Lane-сервером (домен и TLS) → Tailscale Funnel → OpenAI Secure MCP Tunnel. Исходящий трафик туннелей идёт через Lane.
- Хранит `sha256(secret)` до подтверждения и `sha256(token)` после, а не сам секрет.

## Сторона MCPshka
- `/var/lib/mcpshka/lane/link.json` (0600): до подтверждения там `request_id, secret, code`, после — только `port_id, token, public_url`.
- Карточка показывает код, затем адрес `<public_url>/mcp` с кнопкой «Скопировать для ChatGPT/Claude», а также «Новый ключ» и «Удалить». Если Lane отозвал ключ, карточка просит подключить заново.

## Открыто
- OAuth issuer MCPshka сейчас один публичный адрес. Для адреса Lane issuer должен совпадать с хостом, через который пришёл клиент: это следующий шаг.
