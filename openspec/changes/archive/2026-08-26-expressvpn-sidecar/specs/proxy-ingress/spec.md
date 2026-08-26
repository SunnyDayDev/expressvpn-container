## Purpose

Входящий SOCKS5‑прокси — единственная «дверь», через которую клиентские приложения пользователя (на этом хосте или в его сети) отправляют трафик в туннель ExpressVPN.

## ADDED Requirements

### Requirement: SOCKS5 server
Контейнер SHALL предоставлять SOCKS5‑сервер (RFC 1928) с командами CONNECT и UDP ASSOCIATE, адресами IPv4, IPv6 и доменными именами. По умолчанию аутентификация выключена (метод `NO AUTH`); конфигурация `proxy.auth` SHALL позволять включить логин/пароль (RFC 1929) — это обязательно, если порт публикуется не на loopback (удалённая цель). Сервер SHALL слушать фиксированный порт внутри контейнера; на хосте порт SHALL публиковаться на `<адрес привязки>:<порт>`, где адрес по умолчанию `127.0.0.1`, а `<порт>` задаётся переменной в `.env` при развёртывании (по умолчанию `1080`).

#### Scenario: Auth enabled
- **WHEN** `proxy.auth = {username, password}` задан и клиент подключается без учётных данных
- **THEN** сервер отвечает «no acceptable methods» и закрывает соединение; с верными учётными данными — работает как обычно

#### Scenario: TCP via proxy
- **WHEN** приложение открывает `socks5h://127.0.0.1:1080` → `https://example.com`
- **THEN** соединение устанавливается через туннель ExpressVPN, сайт видит IP выбранной локации

#### Scenario: UDP via proxy
- **WHEN** приложение выполняет UDP ASSOCIATE и отправляет DNS‑запрос
- **THEN** датаграмма уходит через туннель и ответ возвращается клиенту

### Requirement: Remote DNS
Доменные имена, переданные клиентом в SOCKS5‑запросе, SHALL резолвиться внутри контейнера через резолвер туннеля ExpressVPN; резолв MUST NOT происходить на хосте.

#### Scenario: DNS leak test via proxy
- **WHEN** через прокси выполняется DNS‑leak тест
- **THEN** видимые DNS‑серверы принадлежат ExpressVPN, а не провайдеру хоста или uplink'а

### Requirement: Fail closed when tunnel is down
Когда туннель ExpressVPN не подключён, сервер SHALL принимать TCP‑соединения, но отвечать на SOCKS5‑запросы кодом `0x03` (Network unreachable); он MUST NOT проксировать трафик напрямую.

#### Scenario: Disconnected VPN
- **WHEN** `state.expressvpn.connection=disconnected` и клиент шлёт CONNECT
- **THEN** ответ SOCKS5 с REP=0x03, соединение закрывается

### Requirement: Connection statistics
Сервер SHALL вести и публиковать в `state.proxy` число активных соединений и суммарные байты в обе стороны с момента запуска контейнера.

#### Scenario: Stats update
- **WHEN** через прокси скачан файл 10 МБ
- **THEN** `state.proxy.bytesIn`/`bytesOut` увеличиваются соответственно, событие приходит по SSE

### Requirement: Host port change requires recreate
Внутренний порт фиксирован; смена порта на хосте SHALL выполняться правкой `.env` и `docker compose up -d` (пересоздание контейнера). Веб‑интерфейс SHALL показывать порт только для чтения с инструкцией (см. web-ui, deployment).

#### Scenario: Change host port
- **WHEN** пользователь меняет `SOCKS_PORT` с 1080 на 1081 в `.env` и выполняет `docker compose up -d`
- **THEN** прокси доступен на `127.0.0.1:1081`, вход в ExpressVPN сохранён
