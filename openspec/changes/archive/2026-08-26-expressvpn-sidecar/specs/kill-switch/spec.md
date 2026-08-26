## Purpose

Гарантирует, что трафик приложений, пришедший во входящий прокси, никогда не покидает контейнер в обход туннеля ExpressVPN, а в режиме `socks5` контейнер не выходит в интернет в обход пользовательского прокси.

## ADDED Requirements

### Requirement: Ingress traffic leaves only through the tunnel
Трафик, порождённый входящим прокси (исходящие соединения и UDP‑датаграммы от имени клиентов прокси), SHALL покидать контейнер только через интерфейс туннеля ExpressVPN. Если туннель не поднят, такие соединения SHALL завершаться ошибкой; они MUST NOT направляться через интерфейс контейнера или uplink‑TUN.

#### Scenario: Tunnel down
- **WHEN** интерфейс туннеля ExpressVPN отсутствует и клиент прокси открывает соединение
- **THEN** соединение завершается ошибкой «сеть недоступна», на интерфейсе контейнера нет пакетов с этим назначением

#### Scenario: Tunnel up
- **WHEN** туннель поднят
- **THEN** трафик клиентов прокси наблюдается только на интерфейсе туннеля

### Requirement: No direct egress in socks5 uplink mode
В режиме `uplink.mode=socks5` через интерфейс контейнера SHALL разрешаться только: трафик к прокси‑эндпоинту, ответы клиентам API и входящего прокси (docker‑подсеть) и loopback. Любой другой исходящий трафик через интерфейс контейнера SHALL блокироваться и учитываться в `state.killswitch.dropped`.

#### Scenario: Uplink TUN lost
- **WHEN** uplink‑TUN исчез, а демон ExpressVPN пытается подключиться к серверу
- **THEN** пакеты к серверу блокируются на интерфейсе контейнера, `killswitch.dropped` растёт, `uplink.status=down`

### Requirement: Rules are atomic and persistent
Правила kill switch SHALL устанавливаться до запуска входящего прокси и демона ExpressVPN и SHALL заменяться атомарно при смене режима uplink'а или эндпоинта. Между старым и новым набором правил MUST NOT существовать окна без защиты.

#### Scenario: Switching uplink
- **WHEN** режим uplink'а меняется с `host` на `socks5`
- **THEN** в каждый момент времени действует либо старый, либо новый набор правил; соединений клиентов прокси через интерфейс контейнера не наблюдается

### Requirement: Kill switch status is observable
`state.killswitch` SHALL сообщать `active=true|false` и счётчик заблокированных пакетов; если правила не удалось применить, агент MUST NOT запускать входящий прокси и SHALL сообщить `lastError` с кодом `killswitch_failed`.

#### Scenario: nftables unavailable
- **WHEN** в контейнере невозможно применить правила фильтрации
- **THEN** входящий прокси не запускается, `state.killswitch.active=false`, `lastError.code=killswitch_failed`

### Requirement: ExpressVPN Network Lock is not relied upon
Агент SHALL держать Network Lock ExpressVPN выключенным, чтобы он не блокировал трафик к uplink‑прокси, и SHALL обеспечивать отсутствие утечек собственными правилами.

#### Scenario: Network Lock re-enabled externally
- **WHEN** Network Lock ExpressVPN оказался включён (например, после обновления демона)
- **THEN** агент выключает его при следующей проверке и пишет предупреждение в лог
