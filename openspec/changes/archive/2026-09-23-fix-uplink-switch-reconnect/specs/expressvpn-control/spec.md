## MODIFIED Requirements

### Requirement: Connect to a location
`connect` SHALL подключать к указанной локации или к `smart`, если локация не задана; пока идёт подключение, `connection = connecting`. Если за 45 секунд подключение не установлено, агент SHALL повторить до 3 раз с экспоненциальной задержкой, затем `connection = error` с кодом `connect_failed`. Смена `expressvpn.location` при подключённом VPN SHALL приводить к переподключению к новой локации через новую сессию демона (см. «Supervision and reconnect»).

#### Scenario: Connect to smart location
- **WHEN** вызван `connect` без локации
- **THEN** VPN подключается к локации, которую демон считает «smart», и `state.expressvpn.location` её показывает

#### Scenario: Unknown location id
- **WHEN** вызван `connect` с `location`, которого нет в списке
- **THEN** операция завершается `failed` с кодом `unknown_location`

#### Scenario: Location change while connected
- **WHEN** VPN подключён и клиент меняет `expressvpn.location` через `PATCH /v1/config`
- **THEN** `connection` проходит `reconnecting → connected`, демон подключён к новой локации, `connectedAt` соответствует моменту нового подключения

### Requirement: Protocol selection with effective fallback
Конфигурация `expressvpn.protocol` SHALL принимать `auto`, `lightway_udp`, `lightway_tcp`, `openvpn_udp`, `openvpn_tcp` и прочие значения, которые поддерживает установленный демон (список из `GET /v1/version`). Если uplink не поддерживает UDP, а запрошен `auto` или UDP‑протокол, агент SHALL применить `lightway_tcp` и отразить это в `protocol.effective`/`protocol.reason`. Когда UDP снова доступен, агент SHALL вернуть запрошенный протокол при следующем подключении.

Если подключение выполняется сразу после перестройки uplink'а в режиме `socks5` с `uplink.socks5.udp=auto`, агент SHALL дождаться результата проверки UDP (не дольше 5 секунд) и только затем выбрать эффективный протокол. Если результата за это время нет, агент SHALL применить `lightway_tcp` с `protocol.reason`, указывающим, что поддержка UDP неизвестна.

Смена `expressvpn.protocol` при подключённом VPN SHALL применяться новой сессией демона, чтобы протокол активной сессии совпадал с `protocol.effective`.

#### Scenario: UDP-less uplink
- **WHEN** `uplink.mode=socks5`, `udpSupported=false`, `protocol=auto`
- **THEN** демон подключается по `lightway_tcp`, `protocol.effective=lightway_tcp`

#### Scenario: Host uplink
- **WHEN** `uplink.mode=host`, `protocol=lightway_udp`
- **THEN** демон подключается по `lightway_udp`, `protocol.reason` пуст

#### Scenario: Switch to a UDP-capable uplink with auto protocol
- **WHEN** VPN подключён, `protocol=auto`, клиент меняет `uplink.socks5.host` на прокси, который поддерживает UDP, при `udp=auto`
- **THEN** после переподключения `protocol.effective=auto`, `protocol.reason` пуст, `uplink.udpSupported=true`

#### Scenario: Protocol change while connected
- **WHEN** VPN подключён по `lightway_tcp`, uplink поддерживает UDP, клиент меняет `expressvpn.protocol` на `lightway_udp`
- **THEN** `connection` проходит `reconnecting → connected`, и демон сообщает, что используется Lightway UDP

### Requirement: Supervision and reconnect
Пока `desired=connected`, агент SHALL отслеживать состояние демона не реже раза в 5 секунд; при неожиданном разрыве SHALL переподключать с экспоненциальной задержкой (1, 2, 4 … до 60 секунд). После 10 подряд неудач `connection=error` с кодом `reconnect_exhausted`; следующая попытка — по действию пользователя или восстановлению uplink'а.

Каждое подключение или переподключение, которое выполняет агент, SHALL устанавливать новую сессию демона. Если демон не в состоянии `Disconnected` (в том числе считает текущую сессию живой или переподключается сам), агент SHALL сначала отключить его и дождаться `Disconnected`, а уже затем подключать. Агент MUST NOT считать подключение установленным по одному лишь ответу демона `Connected`: `connection=connected` и `connectedAt` SHALL выставляться только после того, как демон пришёл в `Connected` после этого нового подключения. Отключение, которое агент выполнил сам в рамках переподключения, MUST NOT отражаться как неожиданный разрыв.

Агент SHALL записывать в журнал на уровне `info` начало каждого переподключения с причиной (например, смена uplink'а, смена локации или протокола, действие пользователя, потеря соединения, повтор после неудачи) и фактическое подключение (локация, протокол, длительность).

#### Scenario: Transient drop
- **WHEN** соединение разорвалось и через 3 секунды сеть доступна
- **THEN** `connection` проходит `reconnecting → connected` без действий пользователя

#### Scenario: Uplink recovered
- **WHEN** `uplink.status` меняется с `down` на `up`, а `desired=connected`
- **THEN** агент немедленно инициирует переподключение

#### Scenario: Daemon still reports a dead session as connected
- **WHEN** агенту нужно переподключиться, а демон отвечает `Connected` по сессии, туннель которой уже не работает
- **THEN** агент отключает демон, дожидается `Disconnected` и подключает заново; `connection` не становится `connected`, пока демон не подключится снова

#### Scenario: Daemon is already reconnecting on its own
- **WHEN** демон сам обнаружил разрыв и находится в `Reconnecting`, а агент начинает переподключение
- **THEN** агент отключает демон и подключает его заново без ошибки `daemon_not_ready`

#### Scenario: Reconnect is logged
- **WHEN** агент переподключает VPN по любой причине
- **THEN** в `GET /v1/logs?component=agent` есть запись `info` о начале переподключения с причиной, а после успеха — запись о подключении с локацией и протоколом
