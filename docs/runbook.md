# Runbook — сборка, проверка утечек, эксплуатация

## Сборка и запуск

```bash
cp .env.example .env          # один раз
docker compose up -d --build  # сборка образа + запуск
docker compose logs -f detour # логи агента (то же — в UI, Diagnostics)
make agent-test               # юнит-тесты агента (Go в контейнере)
make web-dev                  # dev-сервер фронтенда (проксирует API в контейнер)
```

Go и Node на хосте не нужны: агент и фронтенд собираются в multi‑stage Dockerfile
(web-builder: node → agent-builder: go → runtime: debian + ExpressVPN + sing-box).

## Как проверять утечки

1. **Self-check** (Dashboard / Diagnostics): IP через прокси должен отличаться от
   IP uplink'а и принадлежать выбранной стране; вердикт `ok`.
2. **Fail-closed прокси**: при отключённом VPN `curl --socks5-hostname 127.0.0.1:1080 https://example.com`
   должен падать с «Can't complete SOCKS5 connection … (3)» — REP=0x03, а не ходить напрямую.
3. **DNS в режиме socks5**: на eth0 контейнера не должно быть открытых UDP/53:

   ```bash
   docker run --rm --net container:detour-expressvpn nicolaka/netshoot \
     tcpdump -i eth0 -nn udp port 53
   # параллельно порезолвите что-нибудь через прокси/из контейнера — счётчик должен остаться 0
   ```

4. **Трафик клиентов прокси только в туннель**: kill switch дропает помеченные
   пакеты вне `tun0` — счётчик виден в `state.killswitch.dropped` (Dashboard).
5. **Полный ручной чеклист** — сценарий 14.2 в
   `openspec/changes/archive/2026-08-26-expressvpn-sidecar/tasks.md`.

## Сквозной тест переключения uplink'а

`make e2e` проверяет на живом демоне, что смена uplink'а, локации, протокола,
действие `reconnect` и обрыв upstream'а дают новую сессию ExpressVPN, а
`/v1/state` не говорит `connected` раньше времени. Тест идёт по-настоящему
через ExpressVPN, поэтому запускается локально, не в CI.

Предусловия:

- собранный образ: `make build`;
- том с выполненным входом в ExpressVPN. Тест берёт проект compose
  основного чекаута (`E2E_PROJECT=expressvpn-container` → том
  `expressvpn-container_detour-data`) независимо от каталога, поэтому из git
  worktree он попадает в тот же том. Для свежего тома сначала войдите через UI;
- на хосте: `curl`, `jq`, `perl` (на macOS есть из коробки).

Запуск:

```bash
make e2e                  # все сценарии: ~6 минут до Сингапура, зависит от локации
make e2e SCENARIO=s1,s3   # выбранные
```

`make e2e` поднимает detour и два SOCKS5-uplink'а на sing-box из того же образа
(`test/e2e/compose.e2e.yml`), прогоняет сценарии `test/e2e/uplink-switch.sh`
(список и проверки — `test/e2e/uplink-switch.sh --help`) и удаляет socks-контейнеры.
Detour остаётся запущенным. Конфигурацию и желаемое состояние подключения тест
сохраняет в начале и восстанавливает при выходе, в том числе по Ctrl-C.

В конце печатается таблица по сценариям:

- **Disconnected, +с** — когда демон прошёл через `Disconnected` после действия;
- **подключение демона, с** — `Disconnected → Connected` по
  `expressvpnctl monitor connectionstate`. Зависит от локации, сети и хоста;
- **connected, +с** — когда `/v1/state` сообщил `connected`;
- **трафик, +с** — когда трафик через входящий SOCKS5 восстановился. На медленной
  цепочке uplink'а сюда входит и прогрев первых запросов;
- **доля агента, с** — проверяемая величина (порог `AGENT_MAX`, 10 с, спека
  `uplink-routing`): время до `connected` минус фазы демона `disconnect`,
  `connectCmd`, `untilConnected` из записи драйвера `session established`.
  Её фазы печатаются под итогом сценария. Если подключилось не с первой
  попытки (сбой upstream), доля агента не проверяется — в причинах будет пометка.

Против уже развёрнутого Detour (например, на NAS) скрипт запускается напрямую, с
реальными uplink'ами. Docker там доступен через `ssh … sudo`. Сценарий s7
перезапускает upstream и нужен только на локальном стенде:

```bash
E2E_SSH=nas DETOUR_URL=http://192.168.1.89:48100 DETOUR_SOCKS=192.168.1.89:1081 \
E2E_SOCKS_A=192.168.1.1:10808 E2E_SOCKS_B=192.168.1.89:1083 \
  test/e2e/uplink-switch.sh s1,s3,s2
```

Сырые опросы state и монитор демона по каждому сценарию лежат в каталоге,
путь к которому печатается под таблицей. Локации переопределяются через
`E2E_LOCATION`/`E2E_LOCATION_ALT`: для быстрого прогона берите ближнюю.

### Прокси по имени хоста

`make e2e-proxy-name` проверяет путь, который `make e2e` обходит (там uplink'и
заданы IP): смену `uplink.socks5.host` с IP на имя compose-сервиса, когда
`/etc/resolv.conf` уже переписан на перехват sing-box. Имя должно резолвиться
резолвером контейнера, пинованным на старте агента (D5, контур 3). Вход в
ExpressVPN не нужен: стенд собирается из текущего кода в отдельном
compose-проекте (`detour-e2e-proxy-name`, порты 48191/11081) и удаляется по
выходу, рабочий Detour не затрагивается. Занимает меньше минуты.

## Настройка xray для uplink socks5

```jsonc
// xray-inbound на Mac (слушает loopback; из контейнера — host.docker.internal)
{
  "listen": "127.0.0.1",
  "port": 1086,
  "protocol": "socks",
  "settings": { "udp": true }   // без udp:true ExpressVPN пойдёт по TCP
}
```

На Linux/NAS `host.docker.internal` нет — используйте IP docker‑шлюза или общую
docker‑сеть (подробнее в README, раздел NAS).

## Известные ограничения

- **IPv6 выключен** внутри контейнера (sysctl в compose); наружу всё ходит по IPv4
  (v6-сайты доступны через туннель ExpressVPN как обычно).
- **Тройная инкапсуляция** в режиме socks5 (Lightway → SOCKS5 → ваш прокси) режет
  пропускную способность и добавляет задержку — это ожидаемо. При UDP‑прокси
  используется Lightway UDP (быстрее); без UDP — Lightway TCP.
- **UDP/53 наружу может блокироваться сетью/регионом** (наблюдалось в спайках S4):
  probe честно покажет `udpSupported=false`, ExpressVPN уйдёт на TCP.
- **TLS у веб‑интерфейса нет** (вторая волна): пароль + доверенная сеть/за VPN.
- **Сессии UI живут в памяти агента**: пересоздание контейнера разлогинивает
  браузеры (вход в ExpressVPN и конфигурация при этом сохраняются в томе).

## Отладка

- `docker compose exec detour sh` → `LD_LIBRARY_PATH=/opt/expressvpn/lib expressvpnctl -t 10 status`
- Трассировка демона ExpressVPN: `touch /opt/expressvpn/var/.expressvpn-early-debug`
  и перезапуск контейнера → подробный лог в `/opt/expressvpn/var/daemon.log`.
- `GET /v1/logs?component=uplink` — журнал по компонентам (или UI → Diagnostics).
- Диагностический архив: Diagnostics → Download (state/config/logs, секреты вычищены).
