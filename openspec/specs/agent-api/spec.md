## Purpose

HTTP/JSON‑контракт агента внутри контейнера, через который веб‑интерфейс и любой другой клиент (например, скрипты) читают состояние, меняют конфигурацию без перезапуска контейнера и выполняют действия.

## Requirements

### Requirement: Authenticated HTTP endpoint
Агент SHALL обслуживать веб‑интерфейс и HTTP/JSON API с префиксом `/v1` на одном фиксированном порту внутри контейнера (публикация на хост — по `.env`, по умолчанию `127.0.0.1:48100`). Каждый `/v1`‑запрос, кроме `GET /healthz`, SHALL требовать либо валидную cookie‑сессию веб‑интерфейса, либо заголовок `Authorization: Bearer <token>` — кроме случая, когда защита паролем сознательно выключена (состояние `disabled`, см. «Auth endpoints»). API‑токен SHALL генерироваться агентом при первом старте, храниться в томе и быть доступным для просмотра/ротации в UI. Запрос без валидной аутентификации при включённой защите SHALL получать `401`.

При выключенной защите `/v1`‑запросы SHALL обслуживаться без сессии и токена, но изменяющие запросы (методы кроме `GET`/`HEAD`/`OPTIONS`) SHALL отклоняться с `403`, если заголовки браузера указывают на cross‑site происхождение запроса (`Origin` не совпадает с origin агента, или `Sec-Fetch-Site` со значением `cross-site`). Запросы без таких заголовков (curl, скрипты, не‑браузерные клиенты) SHALL проходить без ограничений.

#### Scenario: Missing credentials
- **WHEN** защита паролем включена и клиент вызывает `GET /v1/state` без cookie‑сессии и без заголовка Authorization
- **THEN** ответ `401` с телом `{ "error": "unauthorized" }`

#### Scenario: Token rotation
- **WHEN** пользователь ротирует API‑токен в UI
- **THEN** старый токен получает `401`, cookie‑сессии продолжают работать

#### Scenario: Healthz is public
- **WHEN** клиент вызывает `GET /healthz` без токена
- **THEN** ответ `200` с `{ "status": "ok" }`

#### Scenario: Open access while protection is off
- **WHEN** защита паролем выключена и клиент вызывает `GET /v1/state` без cookie‑сессии и без токена
- **THEN** ответ `200` со снимком состояния

#### Scenario: Cross-site mutation blocked while protection is off
- **WHEN** защита паролем выключена и браузер отправляет `PATCH /v1/config` с заголовком `Origin` чужого сайта (или `Sec-Fetch-Site: cross-site`)
- **THEN** ответ `403`, конфигурация не меняется

### Requirement: Auth endpoints
Защита паролем SHALL иметь три состояния, сохраняемых в томе: `unset` (свежий том — пароль ещё не задавался), `set` (пароль задан, вход обязателен), `disabled` (защита сознательно выключена). Агент SHALL предоставлять: `POST /v1/auth/setup` (создание пароля администратора; доступно в состояниях `unset` и `disabled`, переводит в `set`), `POST /v1/auth/skip` (доступно только в состоянии `unset`, переводит в `disabled`), `POST /v1/auth/disable` (доступно в состоянии `set`; требует текущий пароль, переводит в `disabled` и инвалидирует все сессии), `POST /v1/auth/login` (пароль → HTTP‑only cookie‑сессия), `POST /v1/auth/logout`, `POST /v1/auth/logout-all` (инвалидирует все сессии, включая текущую), `POST /v1/auth/password` (смена пароля; инвалидирует прочие сессии). `GET /v1/auth/status` SHALL быть публичным и возвращать `passwordSet`, `authDisabled`, `authenticated`. Хэш пароля SHALL храниться в томе; изменяющие запросы с cookie‑сессией SHALL требовать CSRF‑токен; после 5 неверных попыток подряд (login или disable) SHALL действовать пауза не менее 30 секунд. Команда `detour-agent reset-password` SHALL возвращать защиту в состояние `unset`.

#### Scenario: Setup only once
- **WHEN** пароль уже задан (`set`) и клиент вызывает `POST /v1/auth/setup`
- **THEN** ответ `409`, пароль не меняется

#### Scenario: Brute-force throttle
- **WHEN** подряд приходят 5 запросов `login` с неверным паролем
- **THEN** следующие попытки в течение 30 секунд получают `429`

#### Scenario: Skip on fresh volume
- **WHEN** состояние `unset` и клиент вызывает `POST /v1/auth/skip`
- **THEN** ответ `200`, `GET /v1/auth/status` возвращает `{"passwordSet": false, "authDisabled": true, "authenticated": true}`, последующие `/v1`‑запросы не требуют аутентификации

#### Scenario: Skip is not available once password is set
- **WHEN** состояние `set` и клиент вызывает `POST /v1/auth/skip`
- **THEN** ответ `409`, защита остаётся включённой

#### Scenario: Disable requires the current password
- **WHEN** состояние `set` и приходит `POST /v1/auth/disable` с неверным паролем
- **THEN** ответ `401`, защита остаётся включённой

#### Scenario: Re-enable after disable
- **WHEN** состояние `disabled` и клиент вызывает `POST /v1/auth/setup` с валидным паролем
- **THEN** ответ `200`, состояние становится `set`, и запрос `GET /v1/state` без сессии и токена получает `401`

### Requirement: Single state snapshot
`GET /v1/state` SHALL возвращать единый снимок состояния: `container` (версия, uptime), `expressvpn` (`auth: logged_in|logged_out`, `connection: disconnected|connecting|connected|reconnecting|error`, текущая локация, `protocol.requested`, `protocol.effective`, `protocol.reason`, публичный IP если известен, время подключения), `uplink` (`mode`, `status: up|down|degraded|unknown`, `udpSupported: true|false|unknown`, эндпоинт без пароля), `proxy` (адрес прослушивания, `status`, активные соединения, счётчики байт), `killswitch` (`active`, счётчик заблокированных пакетов), `lastError` (код, сообщение, время), `desired` (желаемое состояние подключения).

#### Scenario: Connected state
- **WHEN** VPN подключён к локации «Germany - Frankfurt - 1» через `uplink.mode=socks5` без UDP
- **THEN** `state.expressvpn.connection = connected`, `protocol.requested = auto`, `protocol.effective = lightway_tcp`, `protocol.reason = "uplink has no UDP"`, `uplink.status = up`

#### Scenario: Secrets never in state
- **WHEN** сконфигурирован SOCKS5‑uplink с паролем
- **THEN** `state.uplink.endpoint` содержит хост и порт, но не пароль

### Requirement: Event stream
`GET /v1/events` SHALL отдавать Server‑Sent Events с полным снимком `state` при подключении и при каждом изменении состояния не позднее чем через 1 секунду после изменения; каждые 15 секунд SHALL отправляться heartbeat.

#### Scenario: Connection state change
- **WHEN** демон ExpressVPN переходит из `connecting` в `connected`
- **THEN** подписчики `/v1/events` получают событие `state` с обновлённым снимком в течение 1 секунды

### Requirement: Declarative configuration applied live
`GET /v1/config` SHALL возвращать текущую конфигурацию. `PATCH /v1/config` SHALL принимать частичный объект (JSON merge patch), валидировать его целиком и либо применить все изменения, либо не применить ничего. Изменения SHALL применяться без перезапуска контейнера. Ответ SHALL содержать `applied` (список применённых ключей) и `requiresRecreate` (ключи, которые агент не может применить сам — пустой для всех ключей этого API).

Ключи конфигурации: `expressvpn.location`, `expressvpn.protocol`, `expressvpn.autoconnect`, `expressvpn.protections.{ads,trackers,malicious,adult}`, `uplink.mode`, `uplink.socks5.{host,port,username,password,udp}`.

#### Scenario: Change location live
- **WHEN** клиент отправляет `PATCH /v1/config {"expressvpn":{"location":"de-frankfurt-1"}}` при подключённом VPN
- **THEN** ответ `200`, `applied=["expressvpn.location"]`, VPN переподключается к новой локации без перезапуска контейнера

#### Scenario: Invalid value rejected atomically
- **WHEN** клиент отправляет patch с `uplink.mode="wireguard"` и корректным `expressvpn.protocol`
- **THEN** ответ `400` с ошибкой по полю `uplink.mode`, ни одно изменение не применено

#### Scenario: Switching uplink with password
- **WHEN** клиент отправляет `uplink.socks5.password`
- **THEN** пароль сохраняется в томе с правами `0600`, не пишется в логи и не возвращается в `GET /v1/config` (возвращается `"password": "***"` при наличии)

### Requirement: Actions
Агент SHALL предоставлять `POST /v1/actions/<name>` для: `connect` (опционально `{location}`), `disconnect`, `reconnect`, `login` (`{activationCode}`), `logout`, `refresh-locations`, `selfcheck`, `probe-uplink`. Действие SHALL выполняться асинхронно: ответ `202` с `{ "operation": "<id>" }`; прогресс и результат SHALL быть доступны через `GET /v1/operations/<id>` и в событиях. Конфликтующее действие над тем же ресурсом во время выполнения SHALL получать `409`.

`reconnect` SHALL устанавливать новую сессию демона (отключение, затем подключение), даже если демон считает текущую сессию активной. Операция SHALL завершаться `succeeded` только после фактического подключения демона в новой сессии. `disconnect` SHALL завершаться `succeeded` только после того, как демон пришёл в `Disconnected`.

#### Scenario: Login with activation code
- **WHEN** клиент вызывает `login` с корректным кодом активации
- **THEN** операция завершается `succeeded`, `state.expressvpn.auth = logged_in`, код активации не встречается в логах агента

#### Scenario: Login with wrong code
- **WHEN** клиент вызывает `login` с неверным кодом
- **THEN** операция завершается `failed` с кодом `invalid_activation_code` и человекочитаемым сообщением

#### Scenario: Concurrent connect
- **WHEN** операция `connect` выполняется и приходит второй `connect`
- **THEN** второй запрос получает `409` с идентификатором текущей операции

#### Scenario: Reconnect with a session the daemon considers alive
- **WHEN** VPN подключён, туннель фактически не работает, но демон ещё отвечает `Connected`, и клиент вызывает `reconnect`
- **THEN** демон проходит через `Disconnected` и подключается заново, операция завершается `succeeded` только после этого, `connectedAt` соответствует новому подключению

### Requirement: Locations list
`GET /v1/locations` SHALL возвращать список локаций ExpressVPN (`id`, `name`, страна, город, признак `smart`/рекомендованной), кешируемый агентом; `refresh-locations` SHALL обновлять кеш.

#### Scenario: List locations
- **WHEN** клиент вызывает `GET /v1/locations` после входа в аккаунт
- **THEN** ответ содержит непустой список с элементом `smart` и полями `id`, `country`, `city`

### Requirement: Logs
`GET /v1/logs` SHALL возвращать последние N записей с фильтрами `component` (`agent|expressvpn|uplink|proxy|killswitch`) и `since`; `GET /v1/logs/stream` SHALL отдавать их в реальном времени. Записи MUST NOT содержать коды активации, пароли и токены.

#### Scenario: Tail uplink logs
- **WHEN** клиент запрашивает `GET /v1/logs?component=uplink&limit=100`
- **THEN** ответ содержит до 100 последних записей uplink‑движка с временными метками и уровнями

### Requirement: Self-check
Действие `selfcheck` SHALL выполнять и возвращать: (a) публичный IP и страну, видимые через входящий прокси; (b) публичный IP, видимый uplink'ом (то, что видит демон ExpressVPN); (c) DNS‑серверы, отвечающие на запросы через прокси; (d) вердикт `ok|warning|fail` с пояснением (например, «IP через прокси совпадает с IP uplink'а — туннель не используется»).

#### Scenario: Healthy setup
- **WHEN** VPN подключён и прокси работает
- **THEN** IP через прокси принадлежит выбранной стране и отличается от IP uplink'а, вердикт `ok`

#### Scenario: Tunnel down
- **WHEN** VPN отключён
- **THEN** проверка через прокси завершается ошибкой соединения (не утечкой), вердикт `fail` с причиной `tunnel_down`

### Requirement: Version endpoint
`GET /v1/version` SHALL возвращать версии агента, образа, ExpressVPN и uplink‑движка и поддерживаемый набор протоколов ExpressVPN.

#### Scenario: Supported protocols
- **WHEN** демон ExpressVPN поддерживает `wireguard`
- **THEN** список `protocols` содержит `wireguard`, и UI показывает его в выборе протокола
