## 1. Спайки (снимают ключевые риски до основной работы)

- [x] 1.1 S1: собрать минимальный arm64‑образ `debian:trixie-slim` + `.run` ExpressVPN (`--no-gui --sysvinit`), запустить с `NET_ADMIN`/`/dev/net/tun`, убедиться, что демон стартует и `expressvpnctl status` отвечает; зафиксировать в `docs/spikes/S1.md` пути состояния демона, имя init‑скрипта, `getconf PAGESIZE`
- [x] 1.2 S2: в том же контейнере выполнить login/connect и снять `ip route`, `ip rule`, `nft list ruleset`/`iptables-save`, `/etc/resolv.conf` до и после connect; повторить с предварительно выставленным `default dev <tun>` (tun2socks/sing-box к реальному xray‑socks) и убедиться, что маршрут к VPN‑серверу идёт через uplink‑TUN; зафиксировать в `docs/spikes/S2.md`, выбрать mark‑ или uid‑вариант kill switch
- [x] 1.3 S3: собрать реальные выводы `expressvpnctl` (`status`, `get connectionstate`, `get regions`, `get smart`, `get protocol`, `-h`, ошибки login) в `image/agent/internal/xvpn/testdata/` и записать, есть ли JSON‑режим
- [x] 1.4 S4: из контейнера проверить `nc -zv host.docker.internal <port>` к xray‑socks на `127.0.0.1` Mac'а и UDP ASSOCIATE через него; зафиксировать результат и нужные настройки xray (`udp: true`, listen) в `docs/spikes/S4.md`
- [x] 1.5 Сверить design.md с результатами спайков и при необходимости обновить решения D3–D5 (проверка: design.md не содержит утверждений, противоречащих `docs/spikes/*`)

## 2. Каркас репозитория

- [x] 2.1 Создать структуру `image/`, `web/`, `design/`, `docs/`, `docker-compose.yml` + `.env.example`, корневой `README.md` («Detour — ExpressVPN sidecar»: цель, оговорка про лицензию ExpressVPN, быстрый старт) и `Makefile` с целями `up`, `build`, `agent-test`, `web-dev`; проверить, что `make -n` перечисляет цели
- [x] 2.2 Инициализировать Go‑модуль `image/agent` с пакетами `api state config reconciler xvpn uplink killswitch proxy selfcheck logs` и `cmd/detour-agent`; `go vet ./...` проходит внутри `golang` контейнера

## 3. Образ (spec: container-image)

- [x] 3.1 Написать multi‑stage `image/Dockerfile` (builder Go → `debian:trixie-slim`; build‑args `EXPRESSVPN_VERSION`, `SINGBOX_VERSION`; скачивание `.run` и sing-box с проверкой контрольных сумм; `nftables iproute2 ca-certificates`); `docker build` на arm64 проходит, `expressvpnctl`, `sing-box`, `nft` доступны
- [x] 3.2 Добавить в агент проверку `NET_ADMIN`/`/dev/net/tun` с кодом выхода 78 и сообщением; проверить запуском без `--device` и без `--cap-add`
- [x] 3.3 Настроить том `/data` (config, состояние демона, кеш, логи) и HEALTHCHECK на `/healthz`; проверить `docker inspect` → `healthy` при остановленном VPN и сохранение логина после `docker rm` + повторного `run` с тем же томом
- [x] 3.4 Реализовать graceful shutdown по SIGTERM (прокси → disconnect → sing-box → выход ≤10 с); проверить `docker stop` без SIGKILL (`docker inspect .State.ExitCode=0`)
- [x] 3.5 Убедиться, что на eth0 слушают только порты 1080 и 8080 (`ss -ltnp` внутри контейнера), остальное — на loopback

## 4. Агент: ядро и API (spec: agent-api)

- [x] 4.1 Реализовать `state` (единый снимок по спеке, pub/sub) и `config` (JSON в `/data/config.json`, 0600, merge‑patch, валидация всем объектом); unit‑тесты на атомарность и маскирование пароля
- [x] 4.2 Реализовать HTTP‑сервер `/healthz`, `/v1/state`, `/v1/config` (GET/PATCH), `/v1/version` с авторизацией «cookie‑сессия или Bearer» (токен генерируется при первом старте, хранится в `/data/auth/`); тесты: 401 без аутентификации, 200 healthz без неё, 400 на неизвестный `uplink.mode` без частичного применения, ротация токена инвалидирует старый
- [x] 4.3 Реализовать SSE `/v1/events` (снимок при подключении, событие ≤1 с, heartbeat 15 с) — проверить `curl -N` и тест с двумя подписчиками
- [x] 4.4 Реализовать операции `/v1/actions/*` + `/v1/operations/{id}` (асинхронно, 202, 409 при конфликте); тест на параллельный `connect`
- [x] 4.5 Реализовать журнал `/v1/logs` и `/v1/logs/stream` с фильтрами и редактированием секретов (код активации, пароль, токен); тест, что секреты не попадают в вывод
- [x] 4.6 Реализовать реконсайлер `desired → actual` (однопоточный, идемпотентный, пробуждение по изменению config/desired); тест с фейковыми драйверами xvpn/uplink

## 5. ExpressVPN control (spec: expressvpn-control)

- [x] 5.1 Пакет `xvpn`: запуск демона через init‑скрипт/бинарь (по S1), ожидание готовности ≤60 с, `background enable`, `set networklock false`, `set splittunnel false`; проверить по логам и `expressvpnctl status` при старте контейнера
- [x] 5.2 Парсер выводов `expressvpnctl` с golden‑тестами на данных из S3 (`connectionstate`, `regions`, `smart`, `protocol`, ошибки)
- [x] 5.3 Login/logout по API: временный файл 0600, удаление после, коды ошибок `invalid_activation_code`/`already_logged_in`; интеграционная проверка на реальном коде (вручную) + unit‑тест на отсутствие кода в логах
- [x] 5.4 Connect/disconnect/reconnect с таймаутом 45 с, 3 ретраями и `unknown_location`; смена `expressvpn.location` при подключении → переподключение; проверить через `/v1/events`
- [x] 5.5 Протокол: `requested`/`effective`/`reason` по `udpSupported`, список поддерживаемых протоколов в `/v1/version`; unit‑тесты матрицы (host/socks5 × udp on/off/unknown × protocol)
- [x] 5.6 Защита (`ads/trackers/malicious/adult`) и `autoconnect` — применение на лету и при старте; проверить `GET /v1/config` и автоподключение после `docker restart`
- [x] 5.7 Надзор: опрос ≤5 с, перезапуск упавшего демона, реконнект с backoff 1…60 с, `reconnect_exhausted` после 10 неудач, переподключение по восстановлению uplink'а; тест с фейковым демоном
- [x] 5.8 Кеш локаций (`/data/locations.json`, обновление по действию и раз в 24 ч); `GET /v1/locations` содержит `smart`

## 6. Kill switch (spec: kill-switch)

- [x] 6.1 Генератор nftables‑ruleset для режимов `host`/`socks5` (mark `0x5050` → только `tun0`; в socks5 — eth0 только к `<proxy>`/docker‑подсети/lo; счётчики drop); unit‑тесты на текст ruleset, применение через `nft -f` атомарно до старта прокси/демона
- [x] 6.2 Интеграционный leak‑тест в контейнере: при отсутствии `tun0` CONNECT через прокси даёт REP=0x03, `tcpdump -i eth0` не видит пакетов клиентов; при `uplink=socks5` и остановленном sing-box пакеты демона блокируются и `killswitch.dropped` растёт
- [x] 6.3 Периодическая проверка, что Network Lock ExpressVPN выключен, с предупреждением в лог; `killswitch_failed` блокирует запуск прокси — проверить подменой `nft` на неработающий бинарь

## 7. Uplink (spec: uplink-routing)

- [x] 7.1 Пакет `uplink`: генерация конфига sing-box (tun `xup0`, `auto_route: false`, socks outbound с `bind_interface eth0`, DoH через прокси, перехват DNS), запуск/остановка процесса, ожидание интерфейса; проверить `ip link show xup0` и `ip route` по схеме из design.md
- [x] 7.2 Маршруты: `default dev xup0`, `<proxy>/32 via docker-gw`, сохранение eth0‑default с метрикой 1000; резолв имени прокси (в т.ч. `host.docker.internal`) и повторный резолв при сбое; проверить `curl --interface xup0` → IP выхода прокси
- [x] 7.3 UDP‑probe (UDP ASSOCIATE + DNS‑датаграмма, таймаут 3 с), переопределение `udp: auto/on/off`, действие `probe-uplink`; проверить на xray с `udp: true` и `udp: false`
- [x] 7.4 Health‑мониторинг uplink'а каждые 10 с, состояния `up/down/degraded`, уведомление реконсайлера; проверить остановкой/запуском xray: `state.uplink.status` проходит `up→down→up`, VPN переподключается
- [x] 7.5 Живое переключение `host ↔ socks5` по последовательности D7 (≤30 с, защита без окон); проверить `selfcheck.uplinkIP` до/после и отсутствие утечек `tcpdump`'ом
- [x] 7.6 Проверка отсутствия DNS‑утечек в режиме `socks5`: `tcpdump -i eth0 udp port 53` пуст при резолве демоном и при `dig` из контейнера

## 8. Proxy ingress (spec: proxy-ingress)

- [x] 8.1 SOCKS5‑сервер (CONNECT + UDP ASSOCIATE, IPv4/IPv6/domain, NO AUTH) на `0.0.0.0:1080` контейнера, исходящие сокеты с `SO_MARK=0x5050`; тест `curl --socks5-hostname` и UDP‑DNS через прокси
- [x] 8.2 Remote DNS через резолвер туннеля ExpressVPN (источник адреса — по S2) с маркированными сокетами; leak‑тест через прокси показывает DNS ExpressVPN
- [x] 8.3 Fail‑closed: при `connection != connected` — REP=0x03; статистика соединений/байт в `state.proxy`; проверить `curl` при отключённом VPN и рост счётчиков после скачивания 10 МБ
- [x] 8.4 Опциональная аутентификация SOCKS5 (RFC 1929) через `proxy.auth` (по умолчанию выключена); проверить `curl --proxy-user` с верными/неверными данными и отказ без данных при включённой auth

## 9. Self-check (spec: agent-api)

- [x] 9.1 Действие `selfcheck`: IP/страна через прокси, IP uplink'а, DNS через прокси, вердикт `ok/warning/fail` с причинами (`tunnel_down`, `same_ip`); проверить в трёх состояниях: подключён, отключён, uplink down

## 10. Интеграционный прогон контейнера

- [x] 10.1 Скрипт `image/tests/e2e.sh`: сборка, запуск, login (код из env только для теста), connect, проверка IP через прокси ≠ IP хоста, переключение uplink'а, leak‑тесты, `docker stop`; скрипт завершается 0

## 11. Web UI: каркас и авторизация (spec: web-ui, agent-api)

- [x] 11.1 Каркас фронтенда в `web/` (Svelte + Vite), сборка node‑stage'ем в Dockerfile и `go:embed` статики в агент; после `docker compose build` UI открывается с порта агента и не делает ни одного запроса к внешним доменам (проверить вкладкой Network)
- [x] 11.2 Auth в агенте: `POST /v1/auth/{setup,login,logout,logout-all,password}` (argon2id‑хэш в томе, HTTP‑only cookie, CSRF‑токен, троттлинг 5 попыток/30 с) + CLI `detour-agent reset-password` для сброса с хоста + страницы «создать пароль» и «вход» (артборд 11); тесты: 401 без сессии, 409 на повторный setup, 429 после 5 неверных паролей, смена пароля рвёт прочие сессии, reset очищает хэш и сессии
- [x] 11.3 API‑клиент и SSE‑store во фронтенде: единый store состояния, авто‑реконнект SSE, бейдж «агент недоступен», показ/ротация Bearer‑токена в Access; проверить обрывом соединения (docker pause)
- [x] 11.4 i18n en/ru (по языку браузера + переключатель) и адаптивная навигация (Dashboard / Settings / Diagnostics) от 360px; проверить вьюпортом телефона

## 12. Web UI: экраны (spec: web-ui, design/expressvpn-sidecar-design.pen)

- [x] 12.1 Dashboard: hero‑статус, Connect/Disconnect, выбор локации (поиск/Smart/избранные/недавние), переключатель uplink'а host/socks5 в плитке Uplink, блок адреса прокси (хост из URL браузера + опубликованный порт) с Copy, сводка self‑check, браузерные уведомления по opt‑in; live‑обновления ≤1 с; сверить с артбордами 9/9b
- [x] 12.2 Setup‑флоу: пароль → вход в ExpressVPN (inline‑ошибки, код не отображается после отправки) → первое подключение → подсказка настройки приложений (`socks5h://`); сверить с артбордом 10; пройти на чистом томе
- [x] 12.3 Settings: ExpressVPN / Uplink (с «Test uplink» и результатами UDP) / Proxy (auth входящего SOCKS5, статистика) / Container (read‑only) / Access (пароль, logout‑all, API‑токен, язык, уведомления); «живые» поля с inline‑результатом «применено/ошибка»; сверить с артбордами 5–7b
- [x] 12.4 Параметры уровня compose (порты, bind, версии) — read‑only с именем `.env`‑переменной и копируемой командой `docker compose up -d --build`; сверить с артбордами 7/7b
- [x] 12.5 Diagnostics: карточки self‑check, live‑логи с фильтром по компоненту, скачивание диагностического архива; `grep` архива на отсутствие кода активации/паролей/токена; сверить с артбордом 8

## 13. Deployment (spec: deployment)

- [x] 13.1 `docker-compose.yml` + `.env.example` со всеми обязательными параметрами запуска (caps, tun, том, healthcheck, restart, IPv6 off, порты/bind из env); из чистого клона `docker compose up -d --build` → `healthy` → онбординг в браузере
- [x] 13.2 Смена `SOCKS_PORT` в `.env` + `docker compose up -d` → прокси на новом порту, вход в ExpressVPN и настройки сохранены (том)
- [x] 13.3 README quickstart: требования, старт, обновление версии ExpressVPN через `.env`, лицензионная оговорка, раздел NAS (bind на интерфейс, обязательный пароль UI, auth SOCKS5 вне loopback, отсутствие `host.docker.internal` на Linux)
- [x] 13.4 Ручная проверка на Linux‑хосте (NAS или VM): развёртывание по README, UI по адресу хоста, self‑check `ok`; результат зафиксировать в `docs/acceptance.md`

## 14. Документация и финальная проверка

- [x] 14.1 `docs/runbook.md`: как собирать, как проверять утечки (`tcpdump`, self‑check), настройка xray (`udp: true`), известные ограничения (IPv6, скорость тройной инкапсуляции)
- [x] 14.2 Сквозной сценарий вручную: чистый клон → `docker compose up -d --build` → онбординг в браузере (пароль, вход, подключение к другой стране) → браузер через `socks5h://127.0.0.1:1080` показывает IP ExpressVPN, системный трафик — нет → переключение uplink'а на `socks5` без перезапуска контейнера → self‑check `ok`; результат зафиксирован в `docs/acceptance.md`
