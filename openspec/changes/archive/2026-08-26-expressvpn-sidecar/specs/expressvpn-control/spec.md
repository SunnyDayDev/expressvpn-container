## Purpose

Управление демоном ExpressVPN внутри контейнера: вход, подключение, локации, протоколы, защита, автоподключение и надзор за соединением — всё через живой демон, без перезапуска контейнера.

## ADDED Requirements

### Requirement: Daemon bootstrap
При старте контейнера агент SHALL запустить демон ExpressVPN, включить фоновый режим, выключить Network Lock ExpressVPN (его роль выполняет kill switch агента), выключить split tunneling и дождаться готовности демона. Если демон не готов в течение 60 секунд, `state.expressvpn.connection = error` с кодом `daemon_not_ready`.

#### Scenario: Normal start
- **WHEN** контейнер запущен
- **THEN** в течение 60 секунд `expressvpnctl status` отвечает, `state.expressvpn.auth` отражает сохранённую сессию, Network Lock ExpressVPN выключен

#### Scenario: Daemon crash
- **WHEN** процесс демона завершается во время работы
- **THEN** агент перезапускает его, переводит `connection` в `reconnecting` и, если `desired=connected`, переподключает VPN

### Requirement: Login and logout
Вход SHALL выполняться по коду активации, переданному через API. Код SHALL записываться во временный файл с правами `0600` только на время вызова и удаляться сразу после. Выход SHALL отключать VPN и удалять сессию.

#### Scenario: Already logged in
- **WHEN** вызван `login`, а демон уже в аккаунте
- **THEN** операция завершается `succeeded` без повторной активации

#### Scenario: Logout while connected
- **WHEN** вызван `logout` при подключённом VPN
- **THEN** VPN отключается, `auth = logged_out`, `desired = disconnected`

### Requirement: Connect to a location
`connect` SHALL подключать к указанной локации или к `smart`, если локация не задана; пока идёт подключение, `connection = connecting`. Если за 45 секунд подключение не установлено, агент SHALL повторить до 3 раз с экспоненциальной задержкой, затем `connection = error` с кодом `connect_failed`. Смена `expressvpn.location` при подключённом VPN SHALL приводить к переподключению к новой локации.

#### Scenario: Connect to smart location
- **WHEN** вызван `connect` без локации
- **THEN** VPN подключается к локации, которую демон считает «smart», и `state.expressvpn.location` её показывает

#### Scenario: Unknown location id
- **WHEN** вызван `connect` с `location`, которого нет в списке
- **THEN** операция завершается `failed` с кодом `unknown_location`

### Requirement: Protocol selection with effective fallback
Конфигурация `expressvpn.protocol` SHALL принимать `auto`, `lightway_udp`, `lightway_tcp`, `openvpn_udp`, `openvpn_tcp` и прочие значения, которые поддерживает установленный демон (список из `GET /v1/version`). Если uplink не поддерживает UDP, а запрошен `auto` или UDP‑протокол, агент SHALL применить `lightway_tcp` и отразить это в `protocol.effective`/`protocol.reason`. Когда UDP снова доступен, агент SHALL вернуть запрошенный протокол при следующем подключении.

#### Scenario: UDP-less uplink
- **WHEN** `uplink.mode=socks5`, `udpSupported=false`, `protocol=auto`
- **THEN** демон подключается по `lightway_tcp`, `protocol.effective=lightway_tcp`

#### Scenario: Host uplink
- **WHEN** `uplink.mode=host`, `protocol=lightway_udp`
- **THEN** демон подключается по `lightway_udp`, `protocol.reason` пуст

### Requirement: Protection toggles applied live
Переключатели `protections.ads|trackers|malicious|adult` SHALL применяться к демону немедленно, без переподключения, и отражаться в `GET /v1/config`.

#### Scenario: Enable ad blocking
- **WHEN** клиент ставит `protections.ads=true`
- **THEN** демон получает соответствующую настройку, `GET /v1/config` возвращает `ads=true`

### Requirement: Autoconnect
Если `expressvpn.autoconnect=true`, агент SHALL подключать VPN к последней локации при старте контейнера и после восстановления uplink'а; если `false` — ждать явного `connect`.

#### Scenario: Container restart with autoconnect
- **WHEN** контейнер перезапущен, `autoconnect=true`, аккаунт активен
- **THEN** VPN подключается без участия пользователя, `desired=connected`

### Requirement: Supervision and reconnect
Пока `desired=connected`, агент SHALL отслеживать состояние демона не реже раза в 5 секунд; при неожиданном разрыве SHALL переподключать с экспоненциальной задержкой (1, 2, 4 … до 60 секунд). После 10 подряд неудач `connection=error` с кодом `reconnect_exhausted`; следующая попытка — по действию пользователя или восстановлению uplink'а.

#### Scenario: Transient drop
- **WHEN** соединение разорвалось и через 3 секунды сеть доступна
- **THEN** `connection` проходит `reconnecting → connected` без действий пользователя

#### Scenario: Uplink recovered
- **WHEN** `uplink.status` меняется с `down` на `up`, а `desired=connected`
- **THEN** агент немедленно инициирует переподключение

### Requirement: Locations cache
Агент SHALL получать список локаций у демона после входа, кешировать его и обновлять не реже раза в 24 часа и по действию `refresh-locations`.

#### Scenario: Stale cache
- **WHEN** кеш старше 24 часов
- **THEN** агент обновляет его в фоне, не мешая текущему подключению
