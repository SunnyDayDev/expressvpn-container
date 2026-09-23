# API агента Detour

HTTP/JSON API агента внутри контейнера. Это тот же API, которым пользуется
встроенный веб‑интерфейс: всё, что можно сделать в UI, можно сделать запросом.
Нормативный контракт — [openspec/specs/agent-api/spec.md](../openspec/specs/agent-api/spec.md);
этот документ — практический справочник с примерами.

- **Базовый URL**: `http://127.0.0.1:48100` (порт — `HTTP_PORT` из `.env`;
  за reverse proxy — ваш адрес, например `https://expressvpn.home`).
- Все пути API начинаются с `/v1`. Ответы — JSON (`Content-Type: application/json`).
- Изменения конфигурации применяются **на лету**, без пересоздания контейнера.

## Аутентификация

Каждый `/v1`‑запрос (кроме `GET /healthz` и `GET /v1/auth/status`) требует одно из:

- **Bearer‑токен** — для скриптов и агентов: `Authorization: Bearer dtr_…`;
- **cookie‑сессия** — так работает веб‑интерфейс (логин по паролю администратора,
  мутирующие запросы дополнительно требуют CSRF‑заголовок).

Токен генерируется при первом старте контейнера и хранится в томе. Получить его:

- UI → **Settings → Access** (там же — ротация);
- из тома: `docker exec detour-expressvpn cat /data/auth/token`;
- запросом `GET /v1/auth/token` (нужна уже действующая аутентификация).

Ротация (`POST /v1/auth/token/rotate`) немедленно инвалидирует старый токен;
cookie‑сессии продолжают работать.

Если защита паролем **сознательно выключена** (состояние `disabled`), `/v1`
обслуживается без токена. Мутирующие запросы из браузера с чужого origin при
этом отклоняются с `403 {"error":"cross_site_blocked"}` — не‑браузерные клиенты
(curl, скрипты) под этот щит не попадают.

Запрос без валидной аутентификации при включённой защите получает
`401 {"error":"unauthorized"}`.

```bash
TOKEN=$(docker exec detour-expressvpn cat /data/auth/token)
curl -sS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:48100/v1/state | jq
```

## Формат ошибок

Ошибки — JSON‑объект с полем `error` (машинный код или сообщение); ошибки
валидации конфигурации дополнительно содержат `field` и `message`:

```json
{ "error": "validation", "field": "uplink.mode", "message": "unknown mode \"wireguard\" (host|socks5)" }
```

Коды статуса: `400` невалидный запрос, `401` нет аутентификации, `403`
cross‑site‑щит, `404` неизвестный ресурс, `409` конфликт (параллельная операция,
повторный setup пароля), `429` троттлинг перебора пароля, `503` действие
недоступно.

## Обзор эндпоинтов

| Метод и путь | Назначение |
|---|---|
| `GET /healthz` | liveness‑проба, без аутентификации |
| `GET /v1/state` | единый снимок состояния |
| `GET /v1/events` | SSE‑поток снимков состояния |
| `GET /v1/config` | текущая конфигурация (секреты замаскированы) |
| `PATCH /v1/config` | JSON merge patch, «всё или ничего», применяется вживую |
| `POST /v1/actions/{name}` | асинхронное действие → `202` + id операции |
| `GET /v1/operations/{id}` | статус/результат операции |
| `GET /v1/locations` | кешированный список локаций ExpressVPN |
| `GET /v1/logs` | последние записи журнала с фильтрами |
| `GET /v1/logs/stream` | SSE‑поток журнала |
| `GET /v1/version` | версии компонентов и поддерживаемые протоколы |
| `GET /v1/auth/token` | действующий Bearer‑токен |
| `POST /v1/auth/token/rotate` | ротация токена |
| `GET /v1/diagnostics/archive` | zip: state + config (в редакции) + журнал |
| `GET /v1/auth/status` … `POST /v1/auth/password` | управление паролем администратора (см. ниже) |

## `GET /v1/state` — снимок состояния

Возвращает всё состояние одним объектом:

```json
{
  "container":  { "version": "0.1.0", "startedAt": "2026-08-28T09:00:00Z",
                  "publishedSocksPort": "1080", "publishedHTTPPort": "48100",
                  "publishedBindAddr": "127.0.0.1" },
  "expressvpn": { "auth": "logged_in", "connection": "connected",
                  "location": "de-frankfurt-1",
                  "protocol": { "requested": "auto", "effective": "lightway_tcp",
                                "reason": "uplink has no UDP" },
                  "publicIP": "185.xx.xx.xx", "connectedAt": "2026-08-28T09:01:12Z" },
  "uplink":     { "mode": "socks5", "status": "up", "udpSupported": "false",
                  "endpoint": "10.0.0.2:1085" },
  "proxy":      { "listen": "0.0.0.0:1080", "status": "running",
                  "activeConns": 3, "bytesIn": 12345, "bytesOut": 67890 },
  "killswitch": { "active": true, "dropped": 0 },
  "selfcheck":  { "verdict": "ok", "proxyIP": "185.xx.xx.xx", "proxyCountry": "DE",
                  "uplinkIP": "203.xx.xx.xx", "dns": ["10.125.0.1"],
                  "at": "2026-08-28T09:05:00Z" },
  "lastError":  null,
  "desired":    { "connection": "connected" }
}
```

Ключевые словари значений:

- `expressvpn.auth`: `logged_in | logged_out`;
- `expressvpn.connection`: `disconnected | connecting | connected | reconnecting | error`;
  `connected` выставляется только после фактического подключения демона в новой
  сессии (в том числе после смены uplink'а, локации или протокола);
- `expressvpn.connectedAt` — момент установления текущей сессии; во время
  переподключения поля нет;
- `uplink.status`: `up | down | degraded | unknown`; `uplink.udpSupported`: `"true" | "false" | "unknown"`;
- `selfcheck.verdict`: `ok | warning | fail` (поле `null`, пока selfcheck не выполнялся);
- `desired.connection` — желаемое состояние (то, к чему стремится реконсайлер).

Секретов в снимке нет: `uplink.endpoint` — только host:port, без учётных данных.

## `GET /v1/events` — поток состояния (SSE)

`Content-Type: text/event-stream`. При подключении сразу приходит текущий снимок,
далее — событие `state` с полным снимком при каждом изменении (не позднее 1 с);
каждые 15 с — комментарий‑heartbeat.

```bash
curl -sN -H "Authorization: Bearer $TOKEN" http://127.0.0.1:48100/v1/events
```

## Конфигурация

### `GET /v1/config`

Текущая конфигурация. Заданные пароли возвращаются как `"***"`, пустые —
пустыми:

```json
{
  "expressvpn": { "location": "de-frankfurt-1", "protocol": "auto",
                  "autoconnect": true,
                  "protections": { "ads": false, "trackers": false,
                                   "malicious": false, "adult": false } },
  "uplink": { "mode": "socks5",
              "socks5": { "host": "10.0.0.2", "port": 1085, "username": "u",
                          "password": "***", "udp": "auto" } },
  "proxy": { "auth": null }
}
```

### `PATCH /v1/config`

Частичный объект — JSON merge patch (RFC 7386). Патч валидируется **целиком**:
либо применяются все изменения, либо ни одно (`400` с указанием поля). Ответ:

```json
{ "applied": ["expressvpn.location"], "requiresRecreate": [] }
```

`applied` — листовые ключи, которые реально изменились; `requiresRecreate`
всегда пуст (всё, что меняется этим API, применяется без пересоздания;
порты и версии из `.env` в этот API не входят).

| Ключ | Значения | Примечание |
|---|---|---|
| `expressvpn.location` | id локации, `""` | пустая строка = smart‑локация |
| `expressvpn.protocol` | `auto`, `lightway_udp`, `lightway_tcp`, `openvpn_udp`, `openvpn_tcp`, `wireguard` | фактический протокол см. в `state.expressvpn.protocol.effective` |
| `expressvpn.autoconnect` | bool | подключаться при старте контейнера |
| `expressvpn.protections.{ads,trackers,malicious,adult}` | bool | блокировки ExpressVPN |
| `uplink.mode` | `host`, `socks5` | путь контейнера в интернет |
| `uplink.socks5.host` / `.port` | строка / 1–65535 | обязательны при `mode=socks5` |
| `uplink.socks5.username` / `.password` | строка | пароль хранится в томе (0600), не логируется |
| `uplink.socks5.udp` | `auto`, `on`, `off` | `auto` — по результату probe‑uplink |
| `proxy.auth` | `{username, password}` или `null` | аутентификация входящего SOCKS5; `null` — выключить |

Особенности merge patch:

- `null` удаляет/сбрасывает поле (например, `{"proxy":{"auth":null}}` выключает
  аутентификацию прокси);
- значение `"***"` в полях паролей означает «не менять» — объект из
  `GET /v1/config` можно безопасно отправить обратно целиком;
- неизвестные ключи отклоняются (`400`).

```bash
curl -sS -X PATCH -H "Authorization: Bearer $TOKEN" \
  -d '{"expressvpn":{"location":"de-frankfurt-1"}}' \
  http://127.0.0.1:48100/v1/config
```

## Действия и операции

### `POST /v1/actions/{name}`

Действия асинхронные: ответ `202 {"operation":"op_…"}` сразу, результат — через
`GET /v1/operations/{id}` и в потоке событий. Конфликтующее действие над тем же
ресурсом (например, второй `connect`, пока идёт первый) получает
`409 {"error":"conflict","operation":"<id выполняющейся>"}`.

| Действие | Тело | Что делает |
|---|---|---|
| `connect` | `{"location":"…"}` (опц.) | подключиться (к локации или текущей/smart) |
| `disconnect` | — | отключиться; `succeeded` — когда демон фактически в `Disconnected` |
| `reconnect` | — | новая сессия демона (отключение, затем подключение), даже если демон считает текущую живой; `succeeded` — после фактического подключения |
| `login` | `{"activationCode":"…"}` | вход в аккаунт ExpressVPN; код не попадает в логи |
| `logout` | — | выход из аккаунта |
| `refresh-locations` | — | обновить кеш списка локаций |
| `selfcheck` | — | самопроверка; результат — в `state.selfcheck` |
| `probe-uplink` | — | проверить uplink (доступность, поддержка UDP) |

### `GET /v1/operations/{id}`

```json
{ "id": "op_1a2b3c4d5e6f7a8b", "action": "connect", "status": "succeeded",
  "startedAt": "2026-08-28T09:01:00Z", "finishedAt": "2026-08-28T09:01:12Z" }
```

`status`: `running | succeeded | failed`; при `failed` добавляются `errorCode`
(машинный код, например `invalid_activation_code`, `internal`) и `error`
(человекочитаемое сообщение).

Типовой паттерн:

```bash
OP=$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"location":"de-frankfurt-1"}' \
  http://127.0.0.1:48100/v1/actions/connect | jq -r .operation)
curl -sS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:48100/v1/operations/$OP
```

## `GET /v1/locations`

Кешированный список локаций (обновляется действием `refresh-locations`):

```json
{ "locations": [
  { "id": "smart", "name": "Smart Location", "country": "", "city": "", "smart": true },
  { "id": "de-frankfurt-1", "name": "Germany - Frankfurt - 1",
    "country": "Germany", "city": "Frankfurt", "smart": false }
] }
```

## Журнал

### `GET /v1/logs`

Параметры: `component` (`agent | expressvpn | uplink | proxy | killswitch`),
`since` (RFC3339), `limit` (1–5000, по умолчанию 500). Ответ:

```json
{ "entries": [ { "time": "2026-08-28T09:01:12Z", "level": "info",
                 "component": "uplink", "message": "socks5 uplink up" } ] }
```

Секреты (коды активации, пароли, токены) в журнал не попадают — редактируются
на входе.

### `GET /v1/logs/stream`

SSE‑поток событий `log` (те же фильтры, heartbeat каждые 15 с).

## `GET /v1/version`

```json
{ "agent": "0.1.0", "image": "0.1.0", "expressvpn": "3.x", "uplinkEngine": "sing-box 1.x",
  "protocols": ["auto", "lightway_udp", "lightway_tcp", "openvpn_udp", "openvpn_tcp", "wireguard"] }
```

## `GET /v1/diagnostics/archive`

Отдаёт zip (`state.json`, `config.json` в редакции, журнал) — то, что кнопка
«Download diagnostics» в UI. Секретов в архиве нет.

## Управление паролем администратора

Эти эндпоинты в первую очередь обслуживают веб‑интерфейс; скриптам они нужны
редко. Защита имеет три состояния: `unset` (свежий том), `set` (пароль задан),
`disabled` (сознательно выключена).

| Метод и путь | Доступ | Что делает |
|---|---|---|
| `GET /v1/auth/status` | публичный | `{passwordSet, authDisabled, authenticated}` |
| `POST /v1/auth/setup` | `unset`/`disabled` | задать пароль (мин. 8 символов), включить защиту |
| `POST /v1/auth/skip` | только `unset` | выключить защиту на свежем томе |
| `POST /v1/auth/disable` | `set`, нужен текущий пароль | выключить защиту, сбросить все сессии |
| `POST /v1/auth/login` | публичный | `{"password":"…"}` → HTTP‑only cookie‑сессия |
| `POST /v1/auth/logout` | сессия | завершить текущую сессию |
| `POST /v1/auth/logout-all` | сессия/токен | сбросить все сессии |
| `POST /v1/auth/password` | сессия/токен | `{"current","new"}` — смена пароля, прочие сессии сбрасываются |

После 5 неверных попыток подряд (`login`/`disable`) — пауза 30 секунд (`429`).
Забытый пароль сбрасывается только с хоста:
`docker compose exec detour detour-agent reset-password` (см. README).
