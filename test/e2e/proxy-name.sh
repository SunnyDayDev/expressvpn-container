#!/usr/bin/env bash
# Сквозной тест: смена SOCKS5-uplink'а на лету «IP → имя compose-сервиса».
#
#   make e2e-proxy-name
#
# Uplink socks5 на socks-a по IP (агент переписывает /etc/resolv.conf на
# перехват sing-box) → PATCH uplink.socks5.host=socks-b → uplink up. Имя
# прокси агент обязан резолвить резолвером контейнера, пинованным на старте
# (Docker DNS), а не через DoH старого прокси (design D5, контур 3): при
# ленивом пине падало с «lookup socks-b on 172.29.0.2:53: no such host».
# Затем socks5 → host и проверка, что resolv.conf восстановлен.
#
# В отличие от uplink-switch.sh вход в ExpressVPN не нужен: kill switch и
# uplink применяются без него. Стенд собирается из текущего кода в отдельном
# compose-проекте (свои контейнер, порты, том), рабочий Detour не
# затрагивается; по выходу стенд и тестовый образ удаляются. KEEP=1 оставляет
# их для разбора (убрать: docker compose -p detour-e2e-proxy-name down -v --rmi all).
# На хосте: curl, jq. Совместим с системным bash 3.2 (macOS).
set -euo pipefail

cd "$(dirname "$0")/../.."

PROJECT=detour-e2e-proxy-name
CT=detour-e2e-proxy-name # container_name из compose.proxy-name.yml
# Отдельный тег образа: тег рабочего Detour не перезаписывается.
export AGENT_VERSION=e2e-proxy-name
export HTTP_PORT=${HTTP_PORT:-48191} SOCKS_PORT=${SOCKS_PORT:-11081}
API=http://127.0.0.1:$HTTP_PORT
TOKEN=""

say() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() {
	say "FAIL: $*"
	docker logs "$CT" 2>&1 | tail -20 >&2 || true
	exit 1
}

dc() {
	docker compose -p "$PROJECT" -f docker-compose.yml -f test/e2e/compose.e2e.yml \
		-f test/e2e/compose.proxy-name.yml "$@"
}
cleanup() { [ -n "${KEEP:-}" ] || dc down -v --rmi all >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON]
	local args=(-sSf -m 10 -X "$1" -H "Authorization: Bearer $TOKEN")
	[ $# -ge 3 ] && args+=(-H 'Content-Type: application/json' -d "$3")
	curl "${args[@]}" "$API$2"
}

patch_uplink() { api PATCH /v1/config "{\"uplink\":$1}" >/dev/null || die "PATCH uplink $1 rejected"; }

# uplink: «mode status [endpoint]» из /v1/state.
uplink() { api GET /v1/state | jq -r '.uplink | [.mode, .status, .endpoint // empty] | join(" ")'; }

# wait_uplink "<mode> <status> [endpoint]" — ждёт до 30 с. Без VPN всё
# переключение — доля агента (uplink-routing: ≤10 с); остальное — запас.
wait_uplink() {
	local st="" i
	for i in $(seq 1 30); do
		st=$(uplink) || true
		if [ "$st" = "$1" ]; then
			say "uplink: $st"
			return 0
		fi
		sleep 1
	done
	say "uplink: $st (want: $1); lastError: $(api GET /v1/state | jq -c .lastError)"
	return 1
}

nameserver() { docker exec "$CT" awk '$1 == "nameserver" { print $2; exit }' /etc/resolv.conf; }

# ---------- стенд ----------

dc down -v >/dev/null 2>&1 || true
trap cleanup EXIT
say "Сборка образа из текущего кода и запуск стенда ($PROJECT)"
dc build detour
dc up -d

st=""
for i in $(seq 1 30); do
	st=$(docker inspect "$CT" --format '{{.State.Health.Status}}')
	[ "$st" = healthy ] && break
	sleep 2
done
[ "$st" = healthy ] || die "container not healthy: $st"
TOKEN=$(docker exec "$CT" cat /data/auth/token)

ORIG_NS=$(nameserver)
A_IP=$(docker inspect "$(dc ps -q socks-a)" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
say "resolv.conf: nameserver $ORIG_NS; socks-a=$A_IP"

# ---------- сценарий ----------

say "uplink → socks5 socks-a по IP"
patch_uplink "{\"mode\":\"socks5\",\"socks5\":{\"host\":\"$A_IP\",\"port\":1080}}"
wait_uplink "socks5 up $A_IP:1080" || die "socks5 uplink via IP did not come up"
# Предусловие регрессии: резолвер контейнера уже подменён на перехват sing-box.
NS=$(nameserver)
say "resolv.conf: nameserver $NS"
[ "$NS" != "$ORIG_NS" ] || die "resolv.conf was not redirected in socks5 mode"

say "uplink.socks5.host → socks-b (имя compose-сервиса)"
patch_uplink '{"socks5":{"host":"socks-b"}}'
wait_uplink "socks5 up socks-b:1080" ||
	die "proxy name not resolved via pinned container resolver (D5, contour 3)"

say "uplink → host, resolv.conf восстановлен"
patch_uplink '{"mode":"host"}'
wait_uplink "host up" || die "host uplink did not come up"
NS=$(nameserver)
[ "$NS" = "$ORIG_NS" ] || die "resolv.conf not restored: nameserver $NS, want $ORIG_NS"

say "OK — proxy-name прошёл"
