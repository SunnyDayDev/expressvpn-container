## Why

ExpressVPN управляет трафиком на уровне всей системы, а у пользователя уже есть собственный VPN, закрывающий 99 % задач. Нужна возможность точечно пускать трафик отдельных приложений через ExpressVPN (другая страна), не трогая маршруты остальной системы — и при этом ExpressVPN в регионе пользователя доступен только поверх его VPN или SOCKS5‑прокси. Готовые контейнеры ExpressVPN конфигурируются env‑переменными при старте и не решают задачу «uplink через чужой прокси»; macOS‑обвязки к ним нет.

## What Changes

- Новый Docker‑образ `expressvpn-sidecar` (arm64/amd64, Debian slim): ExpressVPN Linux 5.x/14.x (`expressvpnctl` + демон) + агент на Go (PID 1) + sing-box как uplink‑движок.
- Агент внутри контейнера: HTTP/JSON API + SSE на loopback хоста; супервизор демона ExpressVPN, uplink‑движка и SOCKS5‑сервера; единое состояние `state`, все изменения конфигурации применяются **без перезапуска контейнера** (кроме явно помеченных: host‑порты, версия образа).
- Uplink‑маршрутизация контейнера: режимы `host` (через сеть Mac'а) и `socks5` (TUN → ваш SOCKS5, DNS через DoH поверх того же прокси; авто‑фолбэк ExpressVPN на TCP‑протокол при отсутствии UDP у прокси). Архитектура закладывает будущие режимы (`wireguard`, `vless` и т.п.) без переделки.
- Собственный kill switch на nftables (вместо Network Lock ExpressVPN): трафик наружу только через `tun0`, исключения — прокси‑эндпоинт / серверы ExpressVPN / docker‑подсеть для входящего прокси.
- Входящий SOCKS5 (remote DNS, bind `127.0.0.1` хоста по умолчанию) — единственная «дверь» для приложений на Mac'е.
- Встроенный веб‑интерфейс: агент отдаёт SPA на том же HTTP‑порту, что и API. Dashboard с полным потоком (локации, подключение, uplink, адрес прокси, self‑check), настройки ExpressVPN/uplink/прокси, диагностика и логи; вход по паролю администратора (задаётся при первом открытии). Контейнер полностью самодостаточен — тот же UI на Mac и на NAS; нативных приложений нет.
- Развёртывание и жизненный цикл — `docker compose` из репозитория (compose‑файл + `.env`): сборка образа (инсталлятор ExpressVPN скачивается при сборке, не редистрибутируется), создание контейнера, смена портов/версии через правку `.env` и `docker compose up -d --build`.

## Capabilities

### New Capabilities
- `container-image`: состав и сборка образа, требуемые capabilities/devices, тома, healthcheck, версии компонентов.
- `agent-api`: HTTP/SSE‑контракт агента (state, config, actions, logs, self‑check), модель «live vs recreate», аутентификация токеном.
- `expressvpn-control`: управление демоном ExpressVPN через `expressvpnctl` — вход/выход, локации, подключение, протокол, защита, автоподключение, реконнект, перевод в фоновый режим.
- `uplink-routing`: режимы выхода контейнера наружу (`host`, `socks5`), TUN/маршруты/DNS, проверка UDP, переключение на лету, обработка отказов.
- `kill-switch`: правила nftables, поведение при падении туннеля/uplink'а, исключения, гарантии отсутствия утечек.
- `proxy-ingress`: входящий SOCKS5 (+ будущий HTTP), remote DNS, bind/порт, поведение при отсутствии туннеля.
- `web-ui`: встроенный веб‑интерфейс агента — вход/пароль, dashboard, настройки, онбординг первого запуска, диагностика/логи, live‑обновления.
- `deployment`: docker compose, `.env`, README‑quickstart, обновление/пересоздание контейнера, заметки для NAS.

### Modified Capabilities
<!-- нет: проект новый -->

## Impact

- Новые компоненты: `image/` (Dockerfile, agent на Go, конфиги sing-box/nftables), `web/` (SPA; собирается в multi‑stage и встраивается в бинарь агента), `docker-compose.yml` + `.env.example`.
- Внешние зависимости: Docker (Desktop/OrbStack/NAS — любой runtime с `--cap-add NET_ADMIN --device /dev/net/tun` и compose), инсталлятор ExpressVPN Linux (`expressvpn-linux-universal-<ver>_release.run` с expressvpn.works), sing-box; Go и Node нужны только внутри multi‑stage build.
- Требования хоста: любой Docker‑хост (Mac arm64/amd64, Linux x86_64/arm64, NAS); активная подписка ExpressVPN с кодом активации; свой SOCKS5 (xray socks inbound) для режима `socks5`; браузер для UI.
- NAS перестаёт быть «второй волной» по архитектуре: тот же compose разворачивается на NAS, UI доступен по сети (вход по паролю обязателен, работа в доверенной сети/за VPN; TLS — вторая волна). Вне MVP остаётся только TLS.
- Риски, закрывающиеся спайками в tasks: установка `.run` на arm64 в контейнере; поведение маршрутов/iptables демона 14.x поверх uplink‑TUN; наличие JSON у `expressvpnctl`; доступность SOCKS5 на `127.0.0.1` Mac'а из контейнера через `host.docker.internal`.
