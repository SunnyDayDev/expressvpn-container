## Purpose

Определяет состав, сборку и требования к запуску Docker‑образа `expressvpn-sidecar`, в котором работают демон ExpressVPN, управляющий агент, uplink‑движок и входящий SOCKS5‑прокси.

## ADDED Requirements

### Requirement: Image is built locally for the host architecture
Образ SHALL собираться локально из Dockerfile, входящего в состав проекта, для платформ `linux/arm64` и `linux/amd64`. Инсталлятор ExpressVPN Linux SHALL скачиваться с официального адреса ExpressVPN во время сборки; его версия SHALL задаваться build‑аргументом и SHALL входить в тег образа. Собранный образ MUST NOT публиковаться в публичные реестры.

#### Scenario: Build on Apple Silicon
- **WHEN** образ собирается на Mac с Apple Silicon без указания платформы
- **THEN** получается нативный образ `linux/arm64`, в котором доступны `expressvpnctl` и демон ExpressVPN

#### Scenario: ExpressVPN version pinned
- **WHEN** пользователь меняет версию ExpressVPN в `.env`
- **THEN** собирается новый образ с другим тегом, а существующий контейнер помечается как требующий пересоздания

#### Scenario: Installer download fails
- **WHEN** во время сборки инсталлятор ExpressVPN недоступен (сеть/404)
- **THEN** сборка завершается с ошибкой, содержащей URL и HTTP‑статус, частичный образ не создаётся

### Requirement: Runtime prerequisites are verified at start
Контейнер SHALL запускаться с capabilities `NET_ADMIN` и `SYS_PTRACE` (демон ExpressVPN 14.x авторизует IPC‑клиентов чтением `/proc/<pid>/exe` — без `SYS_PTRACE` `expressvpnctl` не работает, см. `docs/spikes/S1.md`) и устройством `/dev/net/tun`. Агент SHALL проверять их наличие при старте и, если чего‑то нет, SHALL завершиться с ненулевым кодом и понятным сообщением в логе.

#### Scenario: Missing TUN device
- **WHEN** контейнер запущен без `/dev/net/tun`
- **THEN** агент пишет в лог `missing /dev/net/tun (add --device /dev/net/tun)` и завершает работу с кодом 78

#### Scenario: Missing NET_ADMIN
- **WHEN** контейнер запущен без `NET_ADMIN`
- **THEN** агент пишет в лог сообщение об отсутствующей capability и завершает работу с кодом 78

### Requirement: ExpressVPN state survives container recreation
Состояние демона ExpressVPN (сессия входа, настройки), конфигурация агента, хэш пароля UI и API‑токен SHALL храниться в именованном томе Docker и переживать пересоздание контейнера.

#### Scenario: Recreate container
- **WHEN** контейнер удалён и создан заново с тем же томом
- **THEN** ExpressVPN остаётся в состоянии «вошёл в аккаунт», повторный ввод кода активации не требуется, применённая конфигурация восстанавливается

#### Scenario: Fresh volume
- **WHEN** контейнер создан с новым пустым томом
- **THEN** состояние — «не вошёл», конфигурация по умолчанию (`uplink.mode=host`, `expressvpn.protocol=auto`)

### Requirement: Only two ports are exposed
Контейнер SHALL слушать на интерфейсе контейнера только два TCP‑порта: входящий SOCKS5 и HTTP‑порт агента (веб‑интерфейс + API). Все прочие внутренние сервисы SHALL слушать только на loopback контейнера. Публикация портов на хост SHALL по умолчанию привязываться к `127.0.0.1` (меняется в `.env` для NAS).

#### Scenario: Port scan of container interface
- **WHEN** сканируются TCP‑порты eth0 контейнера
- **THEN** открыты только порт SOCKS5 и HTTP‑порт агента

### Requirement: Healthcheck reflects agent liveness
Образ SHALL определять HEALTHCHECK, который считает контейнер здоровым, когда агент отвечает на `GET /healthz`. Состояние VPN‑подключения MUST NOT влиять на healthcheck (оно отражается в API).

#### Scenario: Agent alive, VPN disconnected
- **WHEN** агент работает, а ExpressVPN отключён
- **THEN** статус контейнера `healthy`, а `state.expressvpn.connection = disconnected`

### Requirement: Component versions are pinned and reported
Версии агента, sing-box и ExpressVPN SHALL фиксироваться при сборке и быть доступны через API (`GET /v1/version`).

#### Scenario: Query versions
- **WHEN** клиент запрашивает `GET /v1/version`
- **THEN** ответ содержит версии образа, агента, ExpressVPN и uplink‑движка

### Requirement: Graceful shutdown
Агент SHALL быть PID 1 контейнера и по SIGTERM SHALL корректно остановить входящий прокси, отключить VPN, остановить uplink‑движок и завершиться не позднее чем через 10 секунд.

#### Scenario: docker stop
- **WHEN** контейнер получает SIGTERM
- **THEN** все дочерние процессы завершаются, контейнер останавливается без SIGKILL в пределах 10 секунд
