## MODIFIED Requirements

### Requirement: Live mode switching
Смена `uplink.mode` или любых параметров прокси (`uplink.socks5.*`) SHALL применяться без перезапуска контейнера: агент помечает `connection=reconnecting` (если VPN был подключён), перестраивает маршруты/TUN, проверяет uplink и переподключает VPN новой сессией демона — независимо от того, меняется ли при этом эффективный протокол. До фактического подключения демона через новый uplink `connection` MUST NOT быть `connected`, а `connectedAt` MUST NOT обновляться. Во время переключения kill switch SHALL оставаться активным. Доля агента в переключении — время от изменения конфигурации до `connection=connected` за вычетом времени, которое демон тратит на собственное отключение и подключение, — в нормальных условиях SHALL NOT превышать 10 секунд. Длительности этих фаз демона агент SHALL записывать в журнал для каждой новой сессии, чтобы долю агента можно было проверить.

#### Scenario: host → socks5 while connected
- **WHEN** клиент меняет `uplink.mode` с `host` на `socks5` при подключённом VPN
- **THEN** VPN переподключается через прокси, `selfcheck.uplinkIP` меняется на IP выхода прокси, ни один пакет прокси‑клиентов не уходит мимо туннеля

#### Scenario: socks5 → host
- **WHEN** клиент меняет режим обратно на `host`
- **THEN** uplink‑TUN удаляется, маршруты возвращаются к шлюзу Docker, VPN переподключается

#### Scenario: Another SOCKS5 proxy with the same protocol
- **WHEN** VPN подключён по `lightway_tcp`, и клиент меняет `uplink.socks5.host` или `port` на другой работающий прокси, а эффективный протокол не меняется
- **THEN** не позднее чем через 2 секунды после ответа на `PATCH` `connection` становится `reconnecting` или `connecting` и не возвращается в `connected` до фактического подключения; демон проходит через `Disconnected` и подключается заново; доля агента в переключении не превышает 10 секунд

#### Scenario: State during switch is honest
- **WHEN** идёт переключение uplink'а при `desired=connected`
- **THEN** `state.expressvpn.connection` находится в `reconnecting` или `connecting` до фактического подключения, входящий прокси отвечает клиентам кодом SOCKS5 `network unreachable`, а `connectedAt` после завершения соответствует моменту нового подключения
