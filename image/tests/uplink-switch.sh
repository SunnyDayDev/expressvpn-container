#!/usr/bin/env bash
# e2e-регрессия: смена SOCKS5-прокси на лету «IP → имя compose-сервиса».
#
# Uplink socks5 на socks-a по IP (агент переписывает /etc/resolv.conf на
# перехват sing-box) → PATCH host=socks-b → uplink up. Имя прокси обязано
# резолвиться пинованным резолвером контейнера (Docker DNS), а не через DoH
# старого прокси (design D5, контур 3); без пина на старте агента падало с
# «lookup socks-b on 172.29.0.2:53: no such host». В конце — возврат в host
# и восстановление resolv.conf.
#
# Логин ExpressVPN не нужен: kill switch и uplink применяются без него.
# Стенд — отдельный compose-проект со своими контейнером, портами и томом,
# рабочий Detour не затрагивается. По выходу стенд и тестовый образ
# удаляются; KEEP=1 — оставить для разбора
# (убрать потом: docker compose -p detour-uplink-test down -v --rmi all).
#
#   image/tests/uplink-switch.sh   (или make uplink-test)
set -euo pipefail

cd "$(dirname "$0")/../.."

export AGENT_VERSION=uplink-test
export HTTP_PORT=${HTTP_PORT:-48191} SOCKS_PORT=${SOCKS_PORT:-11081}
CT=detour-uplink-test
HTTP=127.0.0.1:$HTTP_PORT

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  docker logs "$CT" 2>&1 | tail -20 >&2 || true
  exit 1
}
dc() { docker compose -p detour-uplink-test -f docker-compose.yml -f image/tests/uplink-switch.compose.yml "$@"; }
cleanup() { [ -n "${KEEP:-}" ] || dc down -v --rmi all >/dev/null 2>&1 || true; }

# socks-прокси запускаются из собираемого образа Detour (в нём есть sing-box).
DETOUR_IMAGE=$(docker compose -f docker-compose.yml config --images)
export DETOUR_IMAGE

say "Сборка $DETOUR_IMAGE и запуск стенда"
dc down -v >/dev/null 2>&1 || true
trap cleanup EXIT
dc build detour
dc up -d

for i in $(seq 1 30); do
  st=$(docker inspect "$CT" --format '{{.State.Health.Status}}')
  [ "$st" = healthy ] && break
  sleep 2
done
[ "$st" = healthy ] || fail "container not healthy: $st"

TOKEN=$(docker exec "$CT" cat /data/auth/token)
api() { curl -sf -m 10 -H "Authorization: Bearer $TOKEN" "http://$HTTP$1" "${@:2}"; }
patch_uplink() {
  api /v1/config -X PATCH -H 'Content-Type: application/json' -d "{\"uplink\":$1}" >/dev/null ||
    fail "PATCH uplink $1 rejected"
}
uplink() {
  api /v1/state | python3 -c 'import json,sys
u = json.load(sys.stdin)["uplink"]
print(" ".join(filter(None, [u["mode"], u["status"], u.get("endpoint")])))'
}
# wait_uplink "<mode> <status> [endpoint]" — ждёт состояния до 30 с (спека
# uplink-routing: переключение в нормальных условиях ≤30 с).
wait_uplink() {
  local st=""
  for i in $(seq 1 30); do
    st=$(uplink) || true
    [ "$st" = "$1" ] && { echo "uplink: $st"; return 0; }
    sleep 1
  done
  echo "uplink: $st (want: $1)" >&2
  api /v1/state | python3 -c 'import json,sys; print("lastError:", json.load(sys.stdin).get("lastError"))' >&2 || true
  return 1
}
nameserver() { docker exec "$CT" awk '$1 == "nameserver" { print $2; exit }' /etc/resolv.conf; }

ORIG_NS=$(nameserver)
A_IP=$(docker inspect "$(dc ps -q socks-a)" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
echo "resolv.conf: nameserver $ORIG_NS; socks-a=$A_IP"

say "uplink → socks5 socks-a по IP"
patch_uplink "{\"mode\":\"socks5\",\"socks5\":{\"host\":\"$A_IP\",\"port\":1080}}"
wait_uplink "socks5 up $A_IP:1080" || fail "socks5 uplink via IP did not come up"
# Предусловие регрессии: резолвер контейнера уже подменён на перехват sing-box.
NS=$(nameserver)
echo "resolv.conf: nameserver $NS"
[ "$NS" != "$ORIG_NS" ] || fail "resolv.conf was not redirected in socks5 mode"

say "uplink.socks5.host → socks-b (имя compose-сервиса)"
patch_uplink '{"socks5":{"host":"socks-b"}}'
wait_uplink "socks5 up socks-b:1080" ||
  fail "proxy name not resolved via pinned container resolver (D5, contour 3)"

say "uplink → host, resolv.conf восстановлен"
patch_uplink '{"mode":"host"}'
wait_uplink "host up" || fail "host uplink did not come up"
NS=$(nameserver)
[ "$NS" = "$ORIG_NS" ] || fail "resolv.conf not restored: nameserver $NS, want $ORIG_NS"

say "OK — uplink-switch прошёл"
