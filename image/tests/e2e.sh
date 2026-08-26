#!/usr/bin/env bash
# e2e-прогон Detour (задача 10.1).
#
# Требует реального кода активации ExpressVPN:
#   EXPRESSVPN_ACTIVATION_CODE=... image/tests/e2e.sh
# или положите его в .local/secrets.env (папка в .gitignore) и запускайте:
#   sh -c 'set -a; . ./.local/secrets.env; set +a; image/tests/e2e.sh'
# Код используется только для login-действия и не сохраняется скриптом.
#
# Прогон: сборка → запуск на чистом томе → login → connect → IP через прокси
# отличается от IP хоста → переключение uplink (если задан E2E_SOCKS5_HOST/PORT)
# → leak-тесты → docker stop. Выход 0 — всё прошло.
set -euo pipefail

cd "$(dirname "$0")/../.."

: "${EXPRESSVPN_ACTIVATION_CODE:?set EXPRESSVPN_ACTIVATION_CODE}"
HTTP=127.0.0.1:${HTTP_PORT:-48100}
SOCKS=127.0.0.1:${SOCKS_PORT:-1080}

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

say "Сборка и чистый запуск"
# Освобождаем слот активации прошлого прогона: down -v стирает локальную
# сессию, не разлогинивая устройство на стороне ExpressVPN.
if docker exec detour-expressvpn true >/dev/null 2>&1; then
  PREV_TOKEN=$(docker exec detour-expressvpn cat /data/auth/token 2>/dev/null || true)
  if [ -n "$PREV_TOKEN" ]; then
    curl -s -m 40 -H "Authorization: Bearer $PREV_TOKEN" -X POST \
      "http://$HTTP/v1/actions/logout" >/dev/null 2>&1 || true
    sleep 5
  fi
fi
docker compose down -v >/dev/null 2>&1 || true
docker compose up -d --build

say "Ожидание healthy"
for i in $(seq 1 30); do
  st=$(docker inspect detour-expressvpn --format '{{.State.Health.Status}}')
  [ "$st" = healthy ] && break
  sleep 2
done
[ "$st" = healthy ] || fail "container not healthy: $st"

TOKEN=$(docker exec detour-expressvpn cat /data/auth/token)
auth=(-H "Authorization: Bearer $TOKEN")

api() { curl -sf "${auth[@]}" "http://$HTTP$1" "${@:2}"; }
action() { # action <name> [json] → ждёт завершения операции
  local body op resp
  # НЕ использовать ${2:-{}}: bash 3.2 (macOS) добавляет лишнюю '}' к телу.
  body="${2:-}"
  [ -n "$body" ] || body='{}'
  resp=$(api "/v1/actions/$1" -X POST -d "$body" || true)
  op=$(python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("operation",""))
except Exception: pass' <<<"$resp")
  [ -n "$op" ] || { echo "action $1 rejected: ${resp:-<empty>}" >&2; return 1; }
  for i in $(seq 1 120); do
    local st
    st=$(api "/v1/operations/$op" | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')
    case $st in
      succeeded) return 0 ;;
      failed) api "/v1/operations/$op" >&2; return 1 ;;
    esac
    sleep 2
  done
  return 1
}

# fetch_ip [curl-опции…] — публичный IP с ретраями по нескольким сервисам.
fetch_ip() {
  local ip
  for attempt in 1 2 3; do
    for url in "https://ifconfig.me/ip" "https://ipinfo.io/ip" "https://icanhazip.com"; do
      ip=$(curl -sf -m 15 "$@" "$url" 2>/dev/null | tr -d '[:space:]') || true
      case "$ip" in
        *[0-9].[0-9]*) echo "$ip"; return 0 ;;
      esac
    done
    sleep 3
  done
  return 1
}

say "Fail-closed до подключения"
if curl -s --socks5-hostname "$SOCKS" -m 5 https://example.com -o /dev/null; then
  fail "proxy passed traffic while disconnected"
fi

say "Login (код из env)"
action login "{\"activationCode\":\"$EXPRESSVPN_ACTIVATION_CODE\"}" || fail "login"

say "Connect (smart)"
action connect || fail "connect"

say "IP через прокси ≠ IP хоста"
HOST_IP=$(fetch_ip) || fail "cannot determine host IP"
VPN_IP=$(fetch_ip --socks5-hostname "$SOCKS") || fail "cannot determine IP via proxy"
echo "host=$HOST_IP vpn=$VPN_IP"
[ -n "$VPN_IP" ] && [ "$VPN_IP" != "$HOST_IP" ] || fail "proxy IP equals host IP"

say "Self-check"
action selfcheck || fail "selfcheck"
api /v1/state | python3 -c 'import json,sys; s=json.load(sys.stdin)["selfcheck"]; assert s["verdict"]=="ok", s; print("verdict:", s["verdict"])'

if [ -n "${E2E_SOCKS5_HOST:-}" ]; then
  say "Переключение uplink → socks5 (${E2E_SOCKS5_HOST}:${E2E_SOCKS5_PORT:-1086})"
  api /v1/config -X PATCH -d "{\"uplink\":{\"mode\":\"socks5\",\"socks5\":{\"host\":\"$E2E_SOCKS5_HOST\",\"port\":${E2E_SOCKS5_PORT:-1086}}}}" >/dev/null
  say "Ожидание uplink up + переподключения VPN (≤180 с)"
  for i in $(seq 1 90); do
    ST=$(api /v1/state | python3 -c 'import json,sys; s=json.load(sys.stdin); print(s["uplink"]["status"], s["expressvpn"]["connection"])')
    [ "$ST" = "up connected" ] && break
    sleep 2
  done
  echo "state: $ST"
  [ "$ST" = "up connected" ] || { api /v1/state >&2; fail "uplink switch did not converge"; }
  # Сразу после переключения туннель может один раз пересесть — ждём окно
  # работоспособности до ~4 минут.
  VPN_IP2=""
  for i in $(seq 1 24); do
    VPN_IP2=$(curl -sf -m 12 --socks5-hostname "$SOCKS" https://ifconfig.me/ip 2>/dev/null | tr -d '[:space:]') && [ -n "$VPN_IP2" ] && break
    sleep 10
  done
  [ -n "$VPN_IP2" ] || fail "no proxy IP in socks5 uplink mode"
  echo "vpn-via-socks5-uplink=$VPN_IP2"

  # Leak-тест выполняется именно в socks5-режиме: в host-режиме DNS к
  # Docker-резолверу через eth0 — штатное поведение. Считаем только
  # ИСХОДЯЩИЕ запросы (dst port 53) от контейнера: поздние входящие ответы
  # из переходного окна утечкой запросов не являются.
  say "Leak-тест: исходящий DNS на eth0 (socks5-режим)"
  sleep 10
  CIP=$(docker exec detour-expressvpn sh -c "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1")
  # Любой образ с tcpdump: спайковый, если остался от разработки, иначе netshoot.
  TCPDUMP_IMG=$(docker images -q detour-spike:s1 | grep -q . && echo detour-spike:s1 || echo nicolaka/netshoot)
  PKTS=$(docker run --rm --net container:detour-expressvpn "$TCPDUMP_IMG" \
    sh -c "timeout 8 tcpdump -i eth0 -nn -l -c 3 \"udp dst port 53 and src host $CIP\" 2>/dev/null" || true)
  if [ -n "$PKTS" ]; then
    echo "$PKTS" >&2
    fail "outbound plaintext DNS seen on eth0 in socks5 mode"
  fi

  say "Uplink → host обратно"
  api /v1/config -X PATCH -d '{"uplink":{"mode":"host"}}' >/dev/null
  sleep 15
fi

say "Disconnect + graceful stop"
action disconnect || fail "disconnect"
docker stop detour-expressvpn >/dev/null
EXIT=$(docker inspect detour-expressvpn --format '{{.State.ExitCode}}')
[ "$EXIT" = 0 ] || fail "exit code $EXIT"
docker compose up -d >/dev/null

say "OK — e2e прошёл"
