# Реальные выводы `expressvpnctl` 14.2.0+13656 (спайк S3)

Сняты 2026-08-25 в контейнере спайка S1 (arm64, debian:trixie-slim, демон 14.2.0.13656).
Файлы `*.txt` — дословный stdout+stderr; строка `rc=N` в конце — код выхода (добавлена
скриптом снятия, в выводе CLI её нет).

## JSON-режима нет

Машинно‑читаемого вывода у `expressvpnctl` **нет**: все `get/set/status` — plain text,
одно значение на строку. Единственное исключение — `speedtest json`. Парсер работает по
текстовым форматам из этих golden‑файлов.

## Коды выхода (наблюдённые)

- `0` — успех (в т.ч. `set`, `disconnect` в состоянии Disconnected)
- `1` — ошибка аргументов/валидации: неизвестный регион, отсутствующий login-файл
- `2` — таймаут ожидания демона (`Timed out after N sec`); также при неавторизованном
  IPC (нет SYS_PTRACE — см. docs/spikes/S1.md)
- `5` — `connect` без входа в аккаунт («This command requires a logged in account»)
- `127` — отказ демона («Request failed, error: 1»): неверный код активации,
  недопустимое значение `set protocol`

## Состояния `get connectionstate`

`Disconnected, Connecting, Connected, Interrupted, Reconnecting,
DisconnectingToReconnect, Disconnecting` (из help).

## Полезное для агента

- `expressvpnctl monitor <type>` — стрим изменений (connectionstate, pubip, region, …):
  можно держать вместо частого поллинга; поллинг остаётся фолбэком.
- `login <file>` — файл с кодом активации (или email+пароль двумя строками).
- `get regions` до входа возвращает только `smart`; `get smart` → `N/A`;
  `get pubip`/`get vpnip` → `Unknown`.
- `status` — многострочный: `Not logged in.` / пустая строка / `Network Lock: …` /
  `Split Tunnel: …`.
- Network Lock по умолчанию «enabled when connected», `set networklock false` → rc=0,
  `get networklock` → `false`.

## Снято после реального входа (2026-08-25)

- `get_regions_logged_in.txt` — 215 строк: `smart`, затем id-регионы
  (`usa-san-francisco`, …) по одному на строку.
- `get_smart_logged_in.txt` — id региона (`usa-san-francisco`).
- `status_connected.txt` — формат: `Connected to Smart (usa-san-francisco)` /
  пустая строка / `Protocol in use: OpenVpnTcp` / `Network Lock: …` /
  `Split Tunnel: …`. После входа, но без подключения первая строка —
  `Location: <region>` (без «Not logged in.»).
- `get_region_smart.txt` — `smart`; `get_vpnip_connected.txt` — IP туннеля.
- `login` успешный: вывод `Logging in with token` (+ rc=0); повторный login при
  активном аккаунте не выполняется (агент проверяет статус заранее).
- `get pubip` обновляется лениво — сразу после connect может отдавать старый
  IP; источником истины для «публичного IP» служит self-check.

## Семантика команд при активной сессии (2026-09-23, 14.2.0.13656)

Снято на локальном стенде: Detour + два socks5-uplink'а (sing-box), протокол
`lightway_tcp`, локация `singapore-marina-bay`; переходы — по
`expressvpnctl monitor connectionstate`.

| Команда | Состояние до | Результат |
|---|---|---|
| `connect <та же локация>` | `Connected` | `rc=0` за 0,12 с, переходов нет — **no-op** |
| `set protocol <другой>` | `Connected` | `rc=0`, переподключения нет, `Protocol in use` не меняется — применится при следующем подключении |
| `set protocol <другой>`, затем `connect <та же локация>` | `Connected` | демон сам: `DisconnectingToReconnect → Reconnecting → Connected` с новым протоколом (e2e s5 до исправления) |
| `connect <другая локация>` | `Connected` | `rc=0` за 0,64 с; демон сам: `DisconnectingToReconnect → Reconnecting → …` |
| `disconnect` | `Connected` | `rc=0` за 0,11 с, возвращается в `Disconnecting`; `Disconnected` — позже (<2 с) |
| `disconnect` | `Reconnecting` | `rc=0` за 0,12 с; `Disconnecting → Disconnected`, без `daemon_not_ready` |
| (sing-box перезапущен под сессией Lightway TCP) | `Connected` | обрыв замечен демоном через 61 с (на NAS — 79–108 с) |
| (upstream-socks перезапущен, sing-box жив) | `Connected` | обрыв замечен за 0,4 с |

Следствия для агента: `connect` и `disconnect` возвращаются до фактического
перехода, а `connect` при «живой» сессии с теми же локацией и протоколом не
делает ничего. Поэтому новая сессия
всегда начинается с `disconnect` и ожидания ровно `Disconnected`
(`Disconnecting` — ещё нет), а успех подключения засчитывается только по
наблюдаемому `Connected` после этого (`Manager.Connect`).
