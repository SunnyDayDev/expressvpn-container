## Context

Мотивация — в `proposal.md`. Технические ограничения, определяющие подход:

- Рабочая машина: Apple Silicon, macOS 26.5, Docker Desktop; целевые хосты — любой Docker (Mac, Linux/NAS). Go и Node на хостах не требуются — агент и фронтенд собираются в multi‑stage Dockerfile.
- ExpressVPN Linux 5.x («universal» `.run`, внутренняя версия 14.x, кодовая база PIA Desktop): демон + `expressvpnctl`; ставится `--no-gui --force-dependencies` (инсталлятор в контейнере не настраивает init‑сервис — демон супервизируется агентом напрямую, стоп‑сигнал SIGINT; S1); официально поддерживает arm64. Демон авторизует IPC‑клиентов чтением `/proc/<pid>/exe` → контейнеру обязателен **`SYS_PTRACE`** (S1). Нативного «uplink через SOCKS5» у демона нет; фаервол демона — iptables‑nft‑цепочки `evpn.*`, свои fwmark `0x3211–0x3214` (S2; с нашей меткой 0x5050 не конфликтуют).
- Docker Desktop поддерживает SOCKS5 для трафика контейнеров только в Business‑подписке → проксирование делаем внутри контейнера.
- Референсы ([Misioslav/expressvpn](https://github.com/Misioslav/expressvpn), polkaned) — bash‑супервизор + env‑конфиг при старте + microsocks. Используем как источник знаний о `expressvpnctl`/подводных камнях (resolv.conf bind‑mount, sysvinit‑скрипт `expressvpn-service`), код не копируем.
- Требования — в `specs/*/spec.md`; здесь только «как».

## Goals / Non-Goals

**Goals:**
- Один контейнер, один управляющий процесс (агент), декларативная конфигурация, применяемая на лету.
- Слоистая сеть внутри контейнера: uplink (host|socks5) → ExpressVPN → входящий SOCKS5, с kill switch, который знает обо всех слоях.
- Контейнер самодостаточен: весь UI — веб‑интерфейс, который отдаёт сам агент; жизненный цикл — docker compose. Один и тот же артефакт разворачивается на Mac и NAS без сопровождающего приложения.
- Архитектурный задел под новые uplink‑режимы (`wireguard`, `vless`, …) и несколько профилей (несколько контейнеров).

**Non-Goals (этот change):**
- HTTP‑прокси наружу (только SOCKS5).
- Прямой uplink в пользовательский VPN (WireGuard/VLESS) — только задел.
- IPv6 внутри контейнера (отключаем sysctl'ом; выходит через IPv4‑туннель).
- Нативных приложений нет и не планируется — весь UI веб.
- TLS для веб‑интерфейса — вторая волна (до тех пор: пароль + доверенная сеть/за VPN).
- Несколько одновременных локаций (профили).

## Decisions

### D1. Управление — агент внутри контейнера: HTTP/SSE API + встроенный веб‑UI
Агент (Go, PID 1) держит единое состояние, применяет конфигурацию транзакционно и сам отдаёт веб‑интерфейс на том же порту, что и API. Клиенты — браузер (основной UI) и скрипты по Bearer‑токену. Альтернативы отвергнуты: нативное macOS‑приложение (дублирует UI‑слой, привязывает контейнер к одной машине — ключевой аргумент против после решения про NAS), `docker exec`‑управление (нет событий, размазывает логику), отдельный контейнер для UI (второй артефакт без выгоды).

### D2. Языки: агент — Go, фронтенд — Svelte + Vite (embed в бинарь)
Агент: один статический Go‑бинарь; нативные SOCKS5‑сервер, SSE, управление процессами, netlink/nftables через `nft -f`; sing-box тоже Go (при желании — встраивание как библиотеки). Bash (референсы) не тянет транзакционную конфигурацию и API; Python тянет рантайм в образ.
Фронтенд: SPA на Svelte, сборка Vite в node‑stage Dockerfile'а, статика попадает в агент через `go:embed` — в рантайме ни Node, ни внешних запросов. Альтернативы: Preact+htm без сборки (хуже DX при живом SSE‑состоянии и i18n), Go‑templates+htmx (много состояния на сервере, сложнее интерактив в выборе локаций). На хосте ни Go, ни Node не нужны — всё в multi‑stage.

### D3. Uplink‑движок — sing-box с TUN‑inbound, `auto_route: false`, маршруты ведёт агент
sing-box выбран вместо tun2socks из‑за встроенного DNS‑слоя (DoH поверх исходящего прокси — решает «SOCKS5 без UDP = демон не может ничего отрезолвить») и набора outbound'ов для будущих режимов. **Важно:** `auto_route` выключен намеренно: sing-box иначе заводит policy‑routing (отдельная таблица + `ip rule`), и демон ExpressVPN, читающий default‑gateway из `main`, проложил бы маршрут к своему серверу через `eth0` в обход uplink'а. Вместо этого агент делает uplink‑TUN (`xup0`) **default route в `main`**, а `proxy_ip/32` — через шлюз Docker; sing-box outbound привязан к `eth0` (`bind_interface`). Тогда любой демон, берущий шлюз из `main`, строит маршрут к VPN‑серверу через `xup0` — и это же работает для policy‑routing‑демонов.

```
  [socks5 mode]  таблица main внутри контейнера
  ─────────────────────────────────────────────────────────────
  0.0.0.0/1, 128.0.0.0/1 dev tun0         ← ставит ExpressVPN (весь трафик приложений → туннель)
  <vpn-server>/32        dev xup0          ← ставит ExpressVPN (шлюз по умолчанию = xup0)
  <proxy-ip>/32 via 172.18.0.1 dev eth0    ← ставит агент (исключение для самого прокси)
  172.18.0.0/16          dev eth0          ← Docker (подсеть: API, входящий прокси, host.docker.internal)
  default                dev xup0          ← ставит агент (uplink)
  [default via 172.18.0.1 dev eth0 metric 1000 — сохраняем с большой метрикой для host-mode отката]
```

Режим `host`: sing-box не запущен, `xup0` отсутствует, default — шлюз Docker.

### D4. Kill switch — собственный, nftables, на основе socket mark
Входящий SOCKS5‑сервер живёт в процессе агента; его исходящие сокеты помечаются `SO_MARK=0x5050` (есть `NET_ADMIN`). Правило `meta mark 0x5050 oifname != "tun0" drop` (+ `oifname lo accept`) гарантирует «трафик клиентов прокси — только в туннель», независимо от того, что случилось с маршрутами. В режиме `socks5` добавляется `oifname eth0` → разрешено только к `<proxy-ip>` (любой порт: UDP‑relay динамический), в docker‑подсеть; остальное — `drop` с счётчиком. Правила грузятся одним `nft -f` (атомная замена таблицы `inet detour`). Network Lock ExpressVPN держим выключенным и периодически перепроверяем. **Спайк S2 подтвердил mark‑вариант**: демон 14.x ставит безусловный `MARK 0x3214` только в mangle PREROUTING (форвард), в OUTPUT метит лишь cgroup'ы split tunnel (выключен) — наша метка не перезаписывается; uid/gid‑вариант остаётся запасным.

### D5. DNS: три независимых контура
1. **Демон ExpressVPN до подключения** (резолв API/серверов): в `host` — резолвер контейнера (Docker → хост); в `socks5` — на время режима агент переписывает `/etc/resolv.conf` на адрес из подсети uplink‑TUN (`172.29.0.2`) → любой запрос уходит в `xup0`, перехватывается `hijack-dns` sing-box'а и обслуживается DoH через прокси (подтверждено: `tcpdump udp/53` на eth0 пуст). Прямое `127.0.0.11` не годится — Docker резолвил бы на хосте в обход прокси.
2. **Клиенты входящего прокси** (remote DNS): агент резолвит имена через DNS туннеля ExpressVPN (адрес — из `resolv.conf`, который переписывает демон при connect; остаток S2), сокеты с `SO_MARK=0x5050` → только `tun0`.
3. **Имя самого прокси‑эндпоинта**: резолвится **пинованным** резолвером контейнера (адрес снят при старте агента, до любых подмен resolv.conf) при применении и при сбое — единственный разрешённый «прямой» резолв.

### D6. Протокол ExpressVPN: `requested` vs `effective`
Пользователь задаёт желаемый протокол; агент вычисляет эффективный из `udpSupported` uplink'а (probe: SOCKS5 UDP ASSOCIATE + реальная DNS‑датаграмма к 1.1.1.1 через relay, таймаут 3 с; xray socks‑inbound умеет UDP, если включён `udp: true`). Без UDP — `lightway_tcp`. Оба значения и причина — в `state`. Это лучше, чем молча подменять настройку или требовать от пользователя знать про UDP.

### D7. Переключение режимов — реконсайлер «desired → actual»
Агент держит `desired` (подключён/отключён, локация, режим uplink) и приводит систему к нему единым циклом с блокировкой; все действия API — это изменение `desired` + операция с прогрессом. Последовательность `host → socks5`:

```
 1. connection=reconnecting; expressvpnctl disconnect
 2. nft -f (правила socks5‑режима)                  ← защита включена до любых маршрутов
 3. resolve proxy host → ip; route add <ip>/32 via <docker-gw>
 4. start sing-box (tun xup0, socks out bind eth0, DoH dns); wait xup0 up
 5. ip route replace default dev xup0
 6. probe uplink (TCP via proxy, UDP ASSOCIATE) → udpSupported, uplink.status
 7. expressvpnctl set protocol <effective>; connect <location>  (если desired=connected)
```
Обратно — зеркально; на любом шаге ошибка → `uplink.status=down`, `lastError`, правила остаются «закрытыми».

### D8. Образ собирается локально через compose
`docker compose build` с build‑args `EXPRESSVPN_VERSION`/`SINGBOX_VERSION` из `.env`; инсталлятор ExpressVPN скачивается при сборке — клиент не редистрибутируется, образ нативен архитектуре хоста (arm64 на Mac, x86_64/arm64 на NAS), версии пинованы. Тег: `detour/expressvpn-sidecar:<evpn>-<agent>`. Публикация в реестры — сознательно нет; перенос на NAS — сборкой на месте (или `docker save/load`).

### D9. Жизненный цикл контейнера — docker compose, не наше ПО
```yaml
services:
  detour:
    build: { context: ./image, args: { EXPRESSVPN_VERSION: "${EXPRESSVPN_VERSION}", SINGBOX_VERSION: "${SINGBOX_VERSION}" } }
    image: detour/expressvpn-sidecar:${EXPRESSVPN_VERSION}-${AGENT_VERSION}
    container_name: detour-expressvpn
    cap_add: [NET_ADMIN, SYS_PTRACE]   # SYS_PTRACE: IPC-авторизация демона (S1)
    devices: ["/dev/net/tun"]
    sysctls: { net.ipv6.conf.all.disable_ipv6: 1 }
    ports:
      - "${BIND_ADDR:-127.0.0.1}:${SOCKS_PORT:-1080}:1080"
      - "${BIND_ADDR:-127.0.0.1}:${HTTP_PORT:-48100}:8080"
    volumes: ["detour-data:/data"]
    restart: unless-stopped
volumes: { detour-data: {} }
```
Всё, что требует пересоздания (порты, bind, версии), живёт в `.env`; применение — `docker compose up -d --build`. Мы не пишем менеджер контейнеров: сравнение «спеки» с реальностью, пересоздание, drift‑детект — работа compose. UI показывает эти параметры read‑only с готовой инструкцией. Профили (несколько стран) в будущем — несколько compose‑сервисов.

### D10. Веб‑интерфейс: страницы, авторизация, live‑состояние
- Страницы: `Setup` (создание пароля → вход в ExpressVPN → первое подключение), `Dashboard` (список локаций + статусная панель — структура артбордов 9/9b), `Settings` (ExpressVPN / Uplink / Proxy / Access), `Diagnostics` (self‑check, live‑логи, архив). Роутинг клиентский, состояние — один store, наполняемый `GET /v1/state` + SSE `/v1/events` с авто‑реконнектом и бейджем «агент недоступен».
- Авторизация: пароль администратора задаётся при первом открытии (`POST /v1/auth/setup`, хэш argon2id в `/data/auth/`), сессии — HTTP‑only cookie + CSRF‑токен (double submit), троттлинг входа. Для скриптов — Bearer‑токен, генерируется при первом старте, просмотр/ротация в Access. Keychain больше не участвует: секреты живут в томе контейнера, как у любого self‑hosted сервиса.
- Адрес прокси в UI строится из хоста адресной строки браузера + опубликованного SOCKS5‑порта (сообщается агенту через env compose) — корректно и на Mac, и на NAS.
- Параметры уровня compose (порты, bind, версии) — read‑only с именем `.env`‑переменной и копируемой командой.
- Уведомления об обрывах — в UI: баннер + Web Notifications API (по разрешению браузера).

### D11. Имя — «Detour», подзаголовок — «ExpressVPN sidecar»
Решение принято (2026‑08‑23): приложение **Detour** (метафора: выбранные приложения делают объезд через другую страну, остальной трафик идёт обычной дорогой), подзаголовок **ExpressVPN sidecar** — в шапке UI (сайдбар), на страницах входа/онбординга, в README и описании репозитория; в имени продукта марка ExpressVPN не используется. Технические идентификаторы остаются описательными: агент `detour-agent`, образ `detour/expressvpn-sidecar`, контейнер `detour-expressvpn`, том `detour-data`, репо/change `expressvpn-sidecar`.

## Architecture

```
 браузер (Mac / телефон / что угодно)              ┌──────────── container: detour-expressvpn ────────────┐
 http://<host>:48100 ────────────────────────────▶│ detour-agent (Go, PID 1)                              │
   Setup · Dashboard · Settings · Diagnostics     │  webui/ (go:embed SPA)  api/  auth/  state/  config/  │
                                                  │  reconciler/                                          │
 скрипты ── Bearer token ────────────────────────▶│  ├─ xvpn/       exec expressvpnctl, poll, parse        │
                                                  │  ├─ uplink/     sing-box cfg+proc, routes, udp probe  │
 docker compose (bootstrap/recreate: .env) ──────▶│  ├─ killswitch/ nft -f (atomic), counters             │
                                                  │  └─ proxy/      SOCKS5 server, SO_MARK, remote DNS    │
 apps ── socks5h://<host>:1080 ──────────────────▶│ :1080 proxy ─▶ tun0 (ExpressVPN) ─▶ xup0|eth0 ─▶ out  │
                                                  └──────────────────────────────────────────────────────┘
```

Состояние подключения: `disconnected → connecting → connected → reconnecting → (connected | error)`; `desired ∈ {connected, disconnected}`. Uplink: `unknown → up | down | degraded`. Любое изменение `config` или `desired` будит реконсайлер; он идемпотентен и однопоточен.

Файлы в томе `/data`: `config.json` (0600, включая пароль uplink'а), `auth/` (argon2id‑хэш пароля UI, API‑токен, 0600), `expressvpn/` (состояние демона — bind в путь, который использует инсталлятор; уточняется спайком S1), `locations.json` (кеш), `logs/` (ротация).

Репозиторий:
```
image/               Dockerfile, agent/ (go module, webui go:embed), singbox/ и nft/ (шаблоны)
web/                 Svelte SPA (Vite), i18n en+ru; собирается node-stage'ем в image/
docker-compose.yml   + .env.example — единственный способ развёртывания
design/              expressvpn-sidecar-design.pen (Pencil), README с перечнем экранов
docs/                runbook (спайки, как проверять утечки), acceptance
```

## UI design

Визуальная фиксация — `design/expressvpn-sidecar-design.pen` (Pencil; перечень и соответствие спекам — `design/README.md`). Дизайн‑система в файле: токены (цвета, тип‑шкала Inter/JetBrains Mono, радиусы) → атомы → молекулы → экраны; макеты «главного окна» и «настроек» напрямую переиспользуются как страницы веб‑интерфейса (структура и компоненты не меняются, меняется только рамка: браузер вместо окна macOS).

Артборды — полноширинные веб‑страницы 1280px в едином фрейме: браузерная полоса с адресом → верхняя навигация сайта (бренд, Dashboard / Settings / Diagnostics, статус‑чип, Sign out) → контент; навигация — общий левый сайдбар приложения (Dashboard · SETTINGS: ExpressVPN / Uplink / Proxy / Container / Access · SYSTEM: Diagnostics):
- **0 · Components** — дизайн‑система.
- **9 · Web — Dashboard (connected)**, **9b · … (disconnected, uplink down)**: слева список локаций, справа hero‑статус, карточки Protocol / Uplink / Traffic, блок адреса прокси с Copy, строка self‑check.
- **10 · Web Setup — Sign in** (центрированная карточка; шаги: пароль администратора → вход в ExpressVPN → первое подключение; inline‑ошибка `invalid_activation_code`; топ‑бар без навигации — до входа), **11 · Web — Login** (вход по паролю, подсказка про сброс через `docker compose exec … reset-password`).
- Мобильная вёрстка (спека: от 360px) артбордом не фиксируется — адаптация каркаса (сайдбар → нижняя навигация/бургер) решается на реализации, задача 11.4.
- **5 · Web Settings — ExpressVPN**, **5b · … Access** (пароль, API‑токен, сессии, язык), **6 · … Uplink**, **7 · … Proxy** (порты read‑only с `.env`‑инструкцией, auth SOCKS5), **7b · … Container** (read‑only, команды compose), **8 · Web — Diagnostics** (отдельная страница верхнего уровня).
Стиль — по референсу продуктового кабинета ExpressVPN+ (скриншот от пользователя, 2026‑08‑23): красный `#DA3940` — только brand‑метка (квадратик логотипа в сайдбаре); все интерактивные элементы зелёные (`accent #0E7C62`, мятная подсветка активного пункта `#E7F2EE`); каркас — левый сайдбар с секциями (SETTINGS / SYSTEM) вместо топ‑навигации; hero на Dashboard — тёмно‑синяя карточка (`#1D3150→#152741`) с белым серифным заголовком (`Lora` — стенд‑ин их дисплейного серифа) и зелёной pill‑кнопкой; текст — Inter, чернильный `#12141F`; инфо‑баннеры голубые (`#E9F1FC`); фон почти белый `#FBFBFD`; карточки без обводки — на мягкой двухслойной тени (границы только у полей ввода и outline‑кнопок), радиус 16, внутренние плитки `#F5F6F8`; правая колонка Dashboard — без карточек: заголовок + серый текст + зелёная ссылка прямо на фоне страницы. Логотип и знак ExpressVPN не используются — только стилистическая близость.

Правила UI: «живые» настройки применяются при изменении с inline‑индикатором «Applied»; параметры уровня compose — только текст (не поля ввода/попапы — ничто не выглядит редактируемым, если UI не может это применить) с бейджем «edit in .env» и копируемой командой; ошибки — `Banner` вверху страницы с одним действием; адрес прокси всегда виден на Dashboard с кнопкой копирования и строится из хоста в адресной строке браузера.

## Вторая волна (за пределами этого change)

- **TLS для UI/API**: reverse‑proxy на NAS или самоподписанный сертификат агента с пиннингом; до тех пор — пароль + доверенная сеть/за VPN.
- **Профили** (несколько стран одновременно): несколько compose‑сервисов + переключатель в UI.
- Замечание для NAS: `host.docker.internal` на Linux отсутствует — xray на NAS указывается по IP docker‑шлюза или общей docker‑сети (зафиксировано в README/deployment).

## Risks / Trade-offs

- [S1: `.run` не ставится/не стартует в arm64 Debian‑контейнере без systemd] → спайк первым в tasks; запасной путь — `--platform linux/amd64` под Rosetta (медленнее, но рабоче).
- [S2: демон 14.x строит маршруты/iptables так, что стыковка с `xup0` или метка `0x5050` ломаются] → спайк: поднять демон в контейнере, снять `ip route/ip rule/nft list ruleset` до/после connect в обоих режимах; запасные варианты — uid‑based kill switch, явный маршрут к серверу (IP из статуса демона).
- [S3: `expressvpnctl` без JSON, формат вывода меняется между версиями] → версия пинована в образе; парсер — в одном пакете с golden‑тестами на реальных выводах; `get connectionstate` используется как источник истины.
- [S4: `host.docker.internal` не достаёт до SOCKS5 на `127.0.0.1` Mac'а] → проверка в спайке; фолбэк — слушать xray на `0.0.0.0` только для интерфейса Docker или указать IP Mac'а в сети Docker.
- [Тройная инкапсуляция (Lightway‑TCP → SOCKS5 → xray) режет пропускную способность и добавляет задержку] → ожидаемо; self‑check показывает путь; при UDP‑прокси используем Lightway‑UDP.
- [MTU/фрагментация вложенных туннелей] → MTU `xup0` 1500 (user‑space TCP‑стек реассемблирует), MTU `tun0` оставляем демону; при проблемах — понижаем в конфиге sing-box.
- [Docker Desktop VM теряет сеть при смене Wi‑Fi/VPN Mac'а] → uplink‑health и реконнект с backoff; проверка `up` после восстановления.
- [Web UI доступен по сети на NAS без TLS] → пароль обязателен (argon2id, троттлинг, CSRF), рекомендация работать в доверенной сети/за VPN; TLS — вторая волна.
- [Лицензия ExpressVPN на автоматизированную установку] → установка на своей машине, для личного пользования; в README — явная оговорка, образ не публикуется.
- [Ошибка в kill switch = утечка] → правила грузятся до старта сервисов; self‑check «IP через прокси ≠ IP uplink'а»; unit‑тесты на генерацию ruleset; ручной чеклист утечек в docs.

## Migration Plan

Greenfield: ничего не мигрируем. Порядок ввода — по `tasks.md` (сначала спайки, затем агент, затем web UI и compose). Откат — `docker compose down` (с `-v` для полного сброса состояния); система хоста не модифицируется. Относительно ранней ревизии change: слой macOS‑приложения удалён до реализации — мигрировать нечего.

## Open Questions

- ~~Где демон 14.x хранит состояние~~ — выяснено (S1): настройки и сессия входа в `/opt/expressvpn/etc` (симлинк на том `/data/expressvpn`), рантайм в `/opt/expressvpn/var`. Как демон правит `resolv.conf` **при connect** — остаток S2 (после первого реального входа); определяет источник DNS для remote‑резолва прокси.
- ~~Есть ли у `expressvpnctl` машинно‑читаемый вывод~~ — нет (S3): plain text, парсер на golden‑тестах; бонус — `expressvpnctl monitor <type>` стримит изменения состояния.
- ~~sing-box: процесс или библиотека~~ — отдельный процесс (запуск/остановка агентом, конфиг генерируется на каждое применение).
