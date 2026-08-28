# Detour — ExpressVPN sidecar

Detour запускает ExpressVPN внутри Docker‑контейнера и выставляет его как обычный
SOCKS5‑прокси. Системные маршруты не трогаются: через другую страну ходят только те
приложения, которым вы явно прописали прокси, — остальной трафик идёт обычной дорогой.

Особенность: контейнер умеет ходить в интернет не только напрямую (`host`), но и через
ваш собственный SOCKS5‑прокси (`socks5`) — для регионов, где сервис ExpressVPN доступен
только поверх вашего VPN/прокси.

Управление — встроенный веб‑интерфейс (пароль администратора задаётся при первом
открытии). Жизненный цикл — docker compose. Один и тот же compose работает на Mac и NAS.

## Зачем

Обычный VPN‑клиент забирает себе всю машину: ставит системный демон, переписывает
маршруты и DNS, добавляет свои правила файрвола — и любой его сбой или «особенность»
затрагивает весь трафик. Идея Detour — полностью изолировать VPN от рабочей машины:
клиент ExpressVPN целиком живёт внутри контейнера, а туннель, маршруты, DNS и
kill switch существуют только в его сетевом неймспейсе и на сеть хоста не влияют.
Снаружи это просто управляемый VPN‑сервис, доступный по SOCKS5: прокси получают
только те приложения, которым вы его явно прописали, — хост остаётся нетронутым.

## Требования

- Docker с compose (Docker Desktop, OrbStack, NAS-докер — любой runtime с
  `--cap-add NET_ADMIN --cap-add SYS_PTRACE --device /dev/net/tun`);
- активная подписка ExpressVPN (код активации со страницы аккаунта);
- для режима `socks5` — свой SOCKS5‑прокси (например, xray socks‑inbound).

## Быстрый старт

```bash
cp .env.example .env
docker compose up -d --build
```

Дальше откройте `http://127.0.0.1:48100/`:

1. задайте пароль администратора;
2. войдите по коду активации ExpressVPN (код нигде не сохраняется и не пишется в логи);
3. подключитесь к локации.

Приложения направляйте в `socks5h://127.0.0.1:1080` (`socks5h` — чтобы DNS резолвился
внутри туннеля, а не на вашей машине).

## Параметры `.env` (применяются пересозданием контейнера)

| Переменная | По умолчанию | Что это |
|---|---|---|
| `SOCKS_PORT` | `1080` | порт SOCKS5‑прокси на хосте |
| `HTTP_PORT` | `48100` | порт веб‑интерфейса и API |
| `BIND_ADDR` | `127.0.0.1` | адрес привязки опубликованных портов |
| `EXPRESSVPN_VERSION` | пинована | версия инсталлятора ExpressVPN Linux |
| `EXPRESSVPN_SHA256` | пинована | контрольная сумма инсталлятора |
| `SINGBOX_VERSION` | пинована | версия uplink‑движка sing-box |

Применение изменений: `docker compose up -d --build`. Вход в ExpressVPN, конфигурация
и пароль UI хранятся в томе `detour-data` и переживают пересоздание.

### Обновление версии ExpressVPN

1. Узнайте новую версию (`expressvpn-linux-universal-<версия>_release.run`).
2. Получите эталонную SHA‑256 — сервер отдаёт её в заголовке:
   `curl -sI https://www.expressvpn.works/clients/linux/expressvpn-linux-universal-<версия>_release.run | grep x-amz-meta-sha256`
3. Обновите `EXPRESSVPN_VERSION` и `EXPRESSVPN_SHA256` в `.env` и выполните
   `docker compose up -d --build`.

## Режим uplink `socks5` (выход через ваш прокси)

Settings → Uplink → `socks5`, укажите хост и порт вашего SOCKS5.

- На Mac прокси, слушающий `127.0.0.1`, указывается как `host.docker.internal`.
- Для UDP (Lightway UDP) в xray‑inbound должно стоять `"udp": true`; без UDP
  Detour автоматически подключает ExpressVPN по `lightway_tcp` (видно в
  `protocol.effective` на дашборде).

## API и навык для Claude Code

Всё, что умеет веб‑интерфейс, доступно по HTTP/JSON API (`/v1` на том же порту):
статус, смена локации и протокола, настройки uplink, selfcheck, логи. Скриптам
нужен Bearer‑токен (UI → Settings → Access). Справочник с примерами —
[docs/api.md](docs/api.md).

```bash
TOKEN=$(docker exec detour-expressvpn cat /data/auth/token)
curl -sS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:48100/v1/state | jq
```

В репозитории публикуется навык для Claude Code —
[.claude/skills/detour](.claude/skills/detour/SKILL.md): агент сможет по просьбе
переключать локации, проверять туннель и читать логи. Репозиторий одновременно
является маркетплейсом плагинов, так что установка — две команды:

```
/plugin marketplace add SunnyDayDev/expressvpn-container
/plugin install detour@detour
```

Настройка (куда положить адрес и токен) — [docs/skill.md](docs/skill.md).

## Развёртывание на NAS (Linux)

### Минимальный набор файлов для сборки

Копировать весь репозиторий не нужно — сборке достаточно:

| Что | Зачем |
|---|---|
| `docker-compose.yml` | описание сервиса и сборки |
| `.env.example` | шаблон для `.env` |
| `.dockerignore` | чистый контекст сборки |
| `image/` | Dockerfile + агент (Go, с `go.mod`/`go.sum`) |
| `web/` | исходники UI (с `package-lock.json` — нужен для `npm ci`) |

Не нужны: `design/`, `docs/`, `openspec/`, `web/node_modules/`, `web/dist/`,
`image/agent/internal/xvpn/testdata/` и тем более `.local/` (секреты).

```bash
rsync -av --exclude web/node_modules --exclude web/dist \
  --exclude 'image/agent/internal/xvpn/testdata' \
  docker-compose.yml .env.example .dockerignore Makefile image web \
  user@nas:/volume1/docker/detour/
```

Дальше на NAS: `cp .env.example .env`, правка `.env` (см. ниже) и
`docker compose up -d --build` — образ собирается нативно для архитектуры NAS
(x86_64/arm64), контрольные суммы sing-box запинованы для обеих.

Если канал NAS до expressvpn.works медленный и сборка падает на скачивании
инсталлятора (202 МБ) — скачайте его заранее и положите в `.build-cache/`
в корне проекта: сборка возьмёт локальный файл (сумма проверяется всё равно).

Старые ядра NAS (4.x без nf_tables) поддерживаются: kill switch сам переходит
на iptables-legacy (в логе — `kill switch applied backend=iptables`).

### Особенности NAS

Тот же compose, но учтите:

- **Привязка к сети.** Поставьте `BIND_ADDR=0.0.0.0` (или IP конкретного
  интерфейса), чтобы UI и прокси были доступны из сети.
- **Пароль UI обязателен** (он и так обязателен) и работайте в доверенной сети —
  TLS в текущей версии нет.
- **Auth на SOCKS5 обязателен вне loopback:** Settings → Local proxy → включите
  логин/пароль (RFC 1929), иначе прокси открыт всей сети.
- **`host.docker.internal` на Linux отсутствует.** Если xray работает на самом
  NAS — указывайте IP docker‑шлюза (обычно `172.17.0.1`) или подключите xray и
  Detour в общую docker‑сеть и используйте имя сервиса.

## Развёртывание на VDS (Linux)

Путь тот же, что и на NAS (скопировать исходники, `docker compose up -d --build`),
но добавляются две темы: тип виртуализации и публикация портов в интернет.

### Требования к машине

| | Минимум | Комфортно |
|---|---|---|
| Виртуализация | KVM | KVM |
| CPU | 1 vCPU | 2 vCPU |
| RAM | 1 GB + 2 GB swap | 2 GB |
| Свободный диск | 10 GB | 20 GB |

Рантайм скромный — замеры idle‑контейнера (arm64, 30 часов uptime):

```
memory.peak  375 MiB   (демон ExpressVPN 294 · sing-box 62 · агент 17)
CPU          ~2,7% одного ядра в среднем
```

Поверх этого docker с containerd (~150 MB) и система — из гигабайта остаётся
немного, поэтому swap на 1 GB‑тарифе обязателен:

```bash
fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile \
  && swapon /swapfile && echo '/swapfile none swap sw 0 0' >> /etc/fstab
```

**Пик нагрузки приходится не на работу, а на сборку.** Стадии `npm ci`/`vite build`
и `go build` плюс распаковка инсталлятора ExpressVPN на 1 GB без swap ловят
OOM‑killer; на диске в это время лежат `golang:1.25-trixie` (~1,3 GB),
`node:22-alpine`, промежуточные слои и build cache — при финальном образе ~600 MB.
На одном ядре сборка занимает порядка 10–15 минут, в основном на скачивании.

Если поднимать тариф ради сборки не хочется — соберите образ на другой машине и
залейте готовым (тег должен совпадать с `image:` из `docker-compose.yml`):

```bash
docker buildx build --platform linux/amd64 -f image/Dockerfile \
  -t detour/expressvpn-sidecar:14.2.0.13656-0.1.0 . --load
docker save detour/expressvpn-sidecar:14.2.0.13656-0.1.0 \
  | gzip | ssh user@vds 'gunzip | docker load'
```

На VDS в этом случае нужны только `docker-compose.yml` и `.env`, а запускать —
`docker compose up -d` без `--build`: compose возьмёт уже загруженный образ.
`--platform` нужен, только если архитектура сборочной машины отличается от VDS
(на Mac стадии node и go пойдут через эмуляцию — работает, но медленно).

### Проверка VDS до установки

Контейнеру нужны `/dev/net/tun`, `NET_ADMIN` и `SYS_PTRACE`. На тарифах с
контейнерной виртуализацией (OpenVZ/Virtuozzo/LXC) TUN обычно недоступен — там
Detour не запустится независимо от объёма памяти. Проверьте это первым делом:

```bash
modprobe tun; ls -l /dev/net/tun   # устройство должно существовать
nft --version                      # нет nftables — kill switch уйдёт на iptables-legacy
```

Второе: VDS должен доставать до серверов ExpressVPN напрямую — иначе понадобится
режим uplink `socks5` через ваш прокси (см. выше).

### Публикация портов

VDS смотрит в интернет, поэтому значение по умолчанию менять не нужно: оставьте
`BIND_ADDR=127.0.0.1` и ходите через SSH‑туннель.

```bash
ssh -L 48100:127.0.0.1:48100 -L 1080:127.0.0.1:1080 user@vds
```

Если доступ снаружи всё же нужен:

- **UI — только за reverse proxy с TLS.** Своего TLS у агента нет, а по HTTP в
  интернет уходят пароль администратора и код активации ExpressVPN.
- **SOCKS5 — только с auth** (Settings → Local proxy) и файрволом, ограниченным
  вашими адресами: открытый прокси на публичном IP найдут за часы.

### Пропускная способность

Одно ядро упирается в шифрование Lightway плюс релей SOCKS5 — для одного
пользователя этого достаточно, для гигабита нет. В режиме uplink `socks5`
(тройная инкапсуляция) запас по CPU нужен заметно больший.

## Сброс пароля администратора

```bash
docker compose exec detour detour-agent reset-password
```

Все сессии становятся недействительными, при следующем входе UI предложит создать
новый пароль.

## Лицензия и распространение

Код проекта — под лицензией [MIT](LICENSE).

Проект не распространяет ПО ExpressVPN: официальный инсталлятор Linux‑клиента
скачивается с сайта ExpressVPN во время сборки образа (с проверкой контрольной
суммы), образ собирается локально и не публикуется в реестры. Используйте с
собственной активной подпиской ExpressVPN для личных нужд; соблюдайте условия
обслуживания ExpressVPN.

## Структура репозитория

- `image/` — Dockerfile и агент (Go): супервизор демона, kill switch (nftables),
  uplink (sing-box), SOCKS5‑сервер, HTTP API + веб‑интерфейс
- `web/` — веб‑интерфейс (Svelte + Vite), собирается в образ и встраивается в агент
- `docker-compose.yml` + `.env.example` — единственный способ развёртывания
- `design/` — макеты интерфейса (Pencil) и их описание
- `docs/` — справочник API, спайки и runbook (сборка, проверка утечек, эксплуатация)
- `.claude/skills/detour/` — навык Claude Code для управления через API
- `openspec/` — спецификации и задачи (OpenSpec)
