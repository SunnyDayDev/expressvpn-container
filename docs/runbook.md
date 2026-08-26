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
