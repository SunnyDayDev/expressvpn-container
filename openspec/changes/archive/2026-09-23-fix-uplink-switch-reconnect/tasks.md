# Tasks: fix-uplink-switch-reconnect

## 1. Семантика CLI и сквозной тест (сначала — базовая линия до исправления)

- [x] 1.1 Перенести таблицу «Поведение `expressvpnctl` при активной сессии» из design.md (Context) в `image/agent/internal/xvpn/testdata/NOTES.md` отдельным разделом с датой и версией демона. Проверка: раздел есть, каждая строка таблицы соответствует замеру со стенда.
- [x] 1.2 Создать `test/e2e/`.
  - Состав: `compose.e2e.yml` (сервисы `socks-a`/`socks-b` из образа detour с `entrypoint: sing-box` и конфигом `socks.json`: socks-inbound → direct, журнал `info`) и `uplink-switch.sh` (bash + `curl` + `jq`).
  - Что делает скрипт: сохраняет `GET /v1/config` и восстанавливает его в `trap`; поднимает `expressvpnctl monitor connectionstate` с метками времени внутри контейнера; опрашивает `/v1/state` раз в секунду и `curl` через `:1080`; печатает таблицу таймингов; код выхода ≠0 при провале.
  - Проверка: `bash -n` проходит, скрипт выводит справку без аргументов.
- [x] 1.3 Реализовать в `uplink-switch.sh` сценарии.

  | # | Сценарий |
  |---|---|
  | S1 | socks5 A→B при закреплённом `lightway_tcp` |
  | S2 | B→A при `protocol=auto`, `udp=auto`: ожидается `effective=auto` без причины |
  | S3 | `POST /v1/actions/reconnect` при рабочем соединении |
  | S4 | смена `expressvpn.location` при подключённом VPN |
  | S5 | смена протокола `lightway_tcp → lightway_udp` при подключённом VPN (`status` → `Protocol in use: LightwayUdp`) |
  | S6 | `socks5 → host` и `host → socks5` |
  | S7 | перезапуск upstream-socks (причина `connection_lost`, без `daemon_not_ready`) |

  Общие проверки для каждого сценария:
  - не позже 2 с после действия `connection ∈ {reconnecting, connecting}`;
  - по монитору демон прошёл через `Disconnected`;
  - `connected` в state появляется не раньше `Connected` в мониторе;
  - `connectedAt` ≥ момента `Connected` в мониторе;
  - накладные расходы (время до трафика минус `Connecting→Connected`) ≤10 с;
  - `killswitch.active=true` на всём протяжении;
  - в `GET /v1/logs?component=agent` есть записи о начале переподключения с ожидаемой причиной и о подключении.

  Выбор сценария: `SCENARIO=s1`.
- [x] 1.4 Добавить в `Makefile` цель `e2e` (поднимает compose с override, ждёт `healthy`, запускает скрипт; `SCENARIO` пробрасывается). Прогнать на локальном Docker на текущем, неисправленном коде: `make build && make e2e`. Проверка: S1, S2, S3, S5 падают; причины и тайминги совпадают с отчётом (ложный `connected`, `connectedAt` сдвинут, простой 60+ с). Вывод сохранить для сравнения в описании PR.

## 2. Драйвер ExpressVPN (`internal/xvpn`)

- [x] 2.1 Модель демона для тестов: `Runner`-автомат по таблице из NOTES. Поведение модели:
  - `connect` при `Connected` с той же локацией ничего не делает; с другой — `DisconnectingToReconnect → Reconnecting → Connected`;
  - `set protocol` при `Connected` откладывается до следующего подключения;
  - `disconnect` возвращается в `Disconnecting` и доходит до `Disconnected` через N опросов, есть режим «зависания»;
  - ведутся счётчик сессий и протокол активной сессии.

  Проверка: собственные тесты модели на каждую строку таблицы проходят (`make agent-test`).
- [x] 2.2 Сырое состояние демона: предикат или метод, отличающий `Disconnected` от `Disconnecting`, пустого и неизвестного вывода; `waitDisconnected` (опрос ~300 мс, таймаут 15 с, `CLIError{Code:"disconnect_failed"}`). Интервалы опроса `waitConnected`/`waitDisconnected` вынести в поля `Manager` с продовыми значениями по умолчанию. Проверка: юнит-тесты на модели — обычное отключение, отключение из `Reconnecting`, зависший `Disconnecting` → `disconnect_failed`.
- [x] 2.3 `Manager.Connect`: на каждой попытке сначала `ensureDisconnected` (если не `Disconnected` → `disconnect` + `waitDisconnected`, сброс `connectedAt`/`publicIP`), затем `set protocol`, `connect`, `waitConnected`, `afterConnect`. Проверка — юнит-тесты на модели:
  - из `Connected` той же локации: `disconnect` до `connect`, новая сессия, `connectedAt` выставлен после перехода;
  - из `Reconnecting`: без ошибки;
  - из `Disconnected`: без лишнего `disconnect`;
  - зависший `Disconnecting`: `connect` не вызывается, ошибка `disconnect_failed`;
  - смена протокола при `Connected`: протокол новой сессии равен запрошенному.
- [x] 2.4 `Manager.Disconnect` завершает работу только после `Disconnected` (тот же помощник), сбрасывает `connectedAt`/`publicIP`. Проверка: юнит-тест, что `Disconnect` не возвращает успех, пока модель в `Disconnecting`.
- [x] 2.5 `Supervise` не считает неожиданным разрывом отключение, которое идёт при `state=reconnecting|connecting`. Проверка: юнит-тест с моделью — при `prev=reconnecting` и демоне в `Disconnected` нет записи `connection dropped` и `state` не меняется.

## 3. Uplink (`internal/uplink`)

- [x] 3.1 `Manager.WaitUDP(ctx) state.TriState`: хранит признак завершения пробы, запущенной последним `Apply`. Для `host` и `udp=on|off` возвращает сразу; при отмене `ctx` — `unknown`. Функция пробы подменяется в тестах. Добавить метод в интерфейс `reconciler.UplinkDriver`. Проверка: юнит-тесты в `uplink_test.go` — ожидание завершения пробы, немедленный возврат для `on`/`off`/`host`, таймаут → `unknown`, повторный `Apply` не отдаёт результат старой пробы.

## 4. Реконсайлер (`internal/reconciler`)

- [x] 4.1 Заменить `forceReconnect bool` на `forceReason string`: шаг 2 ставит `uplink_changed`, `Actions.Reconnect` — `user_request`. Если `forceReason` нет, шаг 4 выводит причину: `location_changed`, `protocol_changed`, `connection_lost`, `retry`, `connect`. Логировать `info`:
  - `reconnecting expressvpn reason=… from=… location=… protocol=…` перед `Connect`;
  - `expressvpn connected location=… protocol=… took=…` после успеха.

  Исправить комментарий в `actions.go`. Проверка: юнит-тесты с перехватом `slog` — для каждой причины есть ожидаемая запись.
- [x] 4.2 UDP перед выбором протокола:
  - шаг 2 сбрасывает `uplink.udpSupported=unknown` до `Apply`, а публикация результата `Apply` не понижает известное значение до `unknown`;
  - шаг 4 при подключении в `socks5` + `udp=auto` + `unknown` вызывает `WaitUDP` с ограничением 5 с, публикует результат и передаёт его в `EffectiveProtocol`.

  Проверка — юнит-тесты:
  - проба вернула `true` через 100 мс → `effective=auto`, `reason` пуст;
  - проба не успела → `lightway_tcp` с причиной `uplink UDP support unknown`;
  - при `desired=disconnected` `WaitUDP` не вызывается.
- [x] 4.3 Обновить fake драйверов в `reconciler_test.go`: журнал порядка вызовов, `Connect`, удерживаемый на канале, управляемый `WaitUDP`. Проверка — тесты:
  - смена uplink'а при подключённом VPN: пока `Connect` удерживается, `connection=reconnecting` и `connectedAt` не меняется; после — `connected`;
  - `Actions.Reconnect` вызывает `Connect` с причиной `user_request` и ждёт завершения;
  - смена локации и смена протокола вызывают `Connect`.

## 5. Документация

- [x] 5.1 `docs/api.md` и `agents-plugin/skills/detour/references/api.md`: `reconnect` всегда создаёт новую сессию и завершается после фактического подключения; `disconnect` — после `Disconnected`; `connectedAt` — момент установления текущей сессии (пусто во время переподключения). Проверка: описания совпадают с дельтой `agent-api`.
- [x] 5.2 `docs/runbook.md`: раздел «Сквозной тест переключения uplink'а» — предусловия (собранный образ, том с выполненным входом), `make e2e`, `SCENARIO=…`, как читать таблицу таймингов, что тест восстанавливает конфигурацию. Проверка: по разделу тест запускается с нуля на Mac.

## 6. Проверка целиком

- [x] 6.1 `go vet ./...` и `go test ./...` в `image/agent` (через `make agent-test` и аналогичный запуск `go vet` в контейнере golang) — зелёные, как в CI.
- [x] 6.2 `make build && make e2e` на локальном Docker: все сценарии S1–S7 проходят. Доля агента ≤10 с в каждом (метрика уточнена в 6.5), `killswitch.active` не падал. Сравнение с базовой линией из 1.4 — в описании PR.
- [x] 6.3 `openspec validate fix-uplink-switch-reconnect --strict` проходит.
- [x] 6.5 По итогам выкатки на NAS (накладные 13,6–17,7 с при пороге 10 с; шаги демона там ~4 с против ~1 с локально): опрос `waitConnected` 2 с → 0,5 с; драйвер журналирует фазы новой сессии (`session established`: disconnect, set protocol, connect, до Connected, afterConnect). Перевыкатить на NAS, повторить s1–s3. Проверка: разбивка накладных по фазам есть в журнале; решение о пороге — по ней. Решение: порог — на долю агента (время до `connected` минус фазы демона), спека `uplink-routing` и e2e обновлены.
- [x] 6.4 После согласования с пользователем: пересборка и выкатка на NAS по `docs/runbook.md`, повтор сценария отчёта (A↔B при `lightway_tcp` и `auto`, опрос `/v1/state` раз в 1–3 с, `curl` через `:1081`). Проверка: `reconnecting` в первые 2 с, доля агента ≤10 с, в журнале есть записи о причине, подключении и фазах (`session established`).
