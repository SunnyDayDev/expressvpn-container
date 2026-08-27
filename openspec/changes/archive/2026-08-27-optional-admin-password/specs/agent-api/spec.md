## MODIFIED Requirements

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
