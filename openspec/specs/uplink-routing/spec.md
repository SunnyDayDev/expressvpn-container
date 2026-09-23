## Purpose

Определяет, как контейнер достигает интернета (серверов и API ExpressVPN): напрямую через сеть хоста или через пользовательский SOCKS5‑прокси, — и как режимы переключаются на лету.

## Requirements

### Requirement: Uplink modes
Конфигурация `uplink.mode` SHALL принимать `host` (по умолчанию) и `socks5`. Значения вне списка SHALL отклоняться с `400`. Схема SHALL допускать добавление новых режимов без изменения существующих ключей.

#### Scenario: Default mode
- **WHEN** контейнер запущен с пустой конфигурацией
- **THEN** `uplink.mode=host`, `uplink.status=up`, uplink‑TUN не создаётся

### Requirement: Host mode
В режиме `host` весь исходящий трафик контейнера SHALL идти через интерфейс контейнера к шлюзу Docker и далее по маршрутам хоста (включая VPN хоста, если он есть); DNS — резолвер контейнера.

#### Scenario: Host VPN active
- **WHEN** Mac подключён к собственному VPN и `uplink.mode=host`
- **THEN** `selfcheck.uplinkIP` равен публичному IP VPN хоста

### Requirement: SOCKS5 mode routes everything through the proxy
В режиме `socks5` весь исходящий трафик контейнера, кроме трафика к самому прокси‑эндпоинту, к docker‑подсети и loopback, SHALL направляться в SOCKS5‑прокси, заданный `uplink.socks5.{host,port,username,password}`. Поле `host` SHALL принимать IP или имя хоста (включая `host.docker.internal`); имя SHALL резолвиться резолвером контейнера в момент применения и повторно при сбое. Никакой трафик контейнера MUST NOT уходить в интернет напрямую через интерфейс контейнера, пока действует режим `socks5`.

#### Scenario: Proxy on the Mac loopback
- **WHEN** `uplink.socks5.host=host.docker.internal`, порт прокси слушает на `127.0.0.1` Mac'а
- **THEN** uplink поднимается, `selfcheck.uplinkIP` равен IP выхода этого прокси

#### Scenario: Proxy unreachable
- **WHEN** прокси не отвечает на TCP‑подключение
- **THEN** `uplink.status=down` с причиной `proxy_unreachable`, трафик ExpressVPN не уходит через интерфейс контейнера напрямую

### Requirement: DNS never leaks past the proxy
В режиме `socks5` DNS‑запросы контейнера SHALL резолвиться через защищённый резолвер (DoH/DoT), проходящий через тот же прокси; открытые UDP/53‑запросы MUST NOT покидать контейнер через интерфейс контейнера. Единственное исключение — резолв имени самого прокси‑эндпоинта.

#### Scenario: Daemon resolves API host
- **WHEN** демон ExpressVPN резолвит имя своего API при `uplink.mode=socks5`
- **THEN** запрос уходит через прокси, на интерфейсе контейнера UDP/53 не наблюдается

### Requirement: UDP capability detection
При применении режима `socks5` и по действию `probe-uplink` агент SHALL проверять поддержку UDP у прокси (SOCKS5 UDP ASSOCIATE и реальная передача UDP‑датаграммы), выставляя `uplink.udpSupported`. Ключ `uplink.socks5.udp` SHALL принимать `auto` (по умолчанию), `on`, `off`, где `on`/`off` переопределяют результат проверки.

#### Scenario: UDP supported
- **WHEN** прокси отвечает на UDP ASSOCIATE и пропускает датаграмму
- **THEN** `udpSupported=true`, протоколы UDP у ExpressVPN не ограничиваются

#### Scenario: UDP not supported
- **WHEN** прокси отклоняет UDP ASSOCIATE или датаграмма не проходит за 3 секунды
- **THEN** `udpSupported=false`, ExpressVPN использует TCP‑протокол (см. expressvpn-control)

### Requirement: Uplink health monitoring
Агент SHALL проверять доступность uplink'а не реже раза в 10 секунд (TCP‑подключение к прокси и проверочный запрос через него). При потере — `uplink.status=down`; при восстановлении — `up` и уведомление слоя ExpressVPN для переподключения.

#### Scenario: Proxy restarts
- **WHEN** прокси недоступен 30 секунд и затем возвращается
- **THEN** `uplink.status` проходит `up → down → up`, VPN переподключается автоматически

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
