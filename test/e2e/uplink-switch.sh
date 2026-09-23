#!/usr/bin/env bash
# Сквозной тест переключения uplink'а и переподключения ExpressVPN
# (openspec change fix-uplink-switch-reconnect).
#
#   make e2e                 # все сценарии
#   make e2e SCENARIO=s1,s3  # выбранные
#
# Предусловия: собранный образ (make build), том с выполненным входом в
# ExpressVPN, запущенные detour + socks-a/socks-b (make e2e поднимает их сам).
# Тест меняет конфигурацию Detour и восстанавливает её при выходе.
# Совместим с системным bash 3.2 (macOS): время — perl, арифметика — awk.
set -uo pipefail

usage() {
	cat <<'EOF'
Usage: test/e2e/uplink-switch.sh all|<scenario>[,<scenario>...]

Сценарии:
  s1  socks5 A→B, протокол закреплён lightway_tcp
  s2  socks5 B→A одновременно с protocol=auto: effective=auto по UDP-пробе
  s3  POST /v1/actions/reconnect при рабочем соединении
  s4  смена expressvpn.location при подключённом VPN
  s5  смена протокола lightway_tcp → lightway_udp при подключённом VPN
  s6  socks5 → host, затем host → socks5
  s7  перезапуск upstream-socks (обрыв замечает демон)

Общие проверки каждого сценария:
  - state уходит из connected в reconnecting|connecting не позже EARLY_MAX;
  - демон проходит через Disconnected (expressvpnctl monitor connectionstate);
  - state=connected не раньше фактического Connected демона;
  - connectedAt не раньше фактического Connected демона;
  - доля агента не больше AGENT_MAX: время от действия до connected в state
    минус фазы демона (disconnect, connectCmd, untilConnected) из записи
    драйвера `session established`; при повторе подключения (сбой upstream)
    порог не проверяется;
  - killswitch.active=true на всём протяжении;
  - в журнале agent есть начало переподключения с причиной и подключение.

Переменные окружения:
  E2E_COMPOSE        команда compose с override (задаёт make e2e): адреса
                     socks-a/socks-b и перезапуск upstream в s7
  E2E_SOCKS_A/B      uplink'и host[:port] (порт по умолчанию 1080) вместо поиска
                     через E2E_COMPOSE
  E2E_SSH            Detour на удалённом хосте: docker через `ssh $E2E_SSH sudo -n
                     $E2E_DOCKER_BIN` (NAS: E2E_SSH=nas)
  E2E_DOCKER_BIN     путь к docker на удалённом хосте (/usr/local/bin/docker)
  DETOUR_URL         API агента (http://127.0.0.1:${HTTP_PORT:-48100})
  DETOUR_SOCKS       входящий SOCKS5 (127.0.0.1:${SOCKS_PORT:-1080})
  DETOUR_CONTAINER   имя контейнера (detour-expressvpn)
  E2E_LOCATION       локация сценариев (по умолчанию — из текущего конфига)
  E2E_LOCATION_ALT   вторая локация для s4 (netherlands-amsterdam)
  AGENT_MAX          допустимая доля агента в переключении, с (10)
  EARLY_MAX          за сколько секунд state обязан уйти из connected (2)
  CONNECT_MAX        предельное ожидание трафика после действия, с (180)
EOF
}

API=${DETOUR_URL:-http://127.0.0.1:${HTTP_PORT:-48100}}
SOCKS=${DETOUR_SOCKS:-127.0.0.1:${SOCKS_PORT:-1080}}
CT=${DETOUR_CONTAINER:-detour-expressvpn}
AGENT_MAX=${AGENT_MAX:-10}
EARLY_MAX=${EARLY_MAX:-2}
CONNECT_MAX=${CONNECT_MAX:-180}
LOC_ALT=${E2E_LOCATION_ALT:-netherlands-amsterdam}
# Uplink'и задаются IP-адресами, как в боевой конфигурации: имя прокси агент
# резолвит сам, и это отдельный путь, который здесь не проверяется.
SOCKS_A=${E2E_SOCKS_A:-}
SOCKS_B=${E2E_SOCKS_B:-}
PROBE_URL=https://api.ipify.org
ALL_SCENARIOS="s1 s3 s5 s2 s4 s7 s6"

TOKEN=""
MON_PID=""
LOC=""
WORK=""
FAILED=0

# ---------- утилиты ----------

now() { perl -MTime::HiRes=time -e 'printf "%.3f", time'; }
calc() { awk "BEGIN { printf \"%.1f\", $1 }"; }
le() { awk -v a="$1" -v b="$2" 'BEGIN { exit !(a <= b) }'; }
say() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() {
	say "FATAL: $*"
	exit 1
}
epoch2iso() { jq -rn --argjson t "$1" '$t | floor | todate'; }

# dur2s: длительность Go (1m2.5s, 350ms, 7.6s) → секунды.
dur2s() {
	awk -v d="$1" 'BEGIN {
		t = 0
		while (match(d, /^[0-9.]+(ms|m|s|h)/)) {
			tok = substr(d, 1, RLENGTH); d = substr(d, RLENGTH + 1)
			if (tok ~ /ms$/) t += tok / 1000
			else if (tok ~ /h$/) t += tok * 3600
			else if (tok ~ /m$/) t += tok * 60
			else t += tok
		}
		printf "%.2f", t
	}'
}

# phase <key>: значение поля из записи `session established` (PHASES).
phase() { awk -v k="$1" '{ for (i = 1; i <= NF; i++) { n = index($i, "="); if (substr($i, 1, n - 1) == k) print substr($i, n + 1) } }' <<<"$PHASES"; }

api() { # api METHOD PATH [JSON]
	local args=(-sS -m 200 -X "$1")
	[ -n "$TOKEN" ] && args+=(-H "Authorization: Bearer $TOKEN")
	[ $# -ge 3 ] && args+=(-H 'Content-Type: application/json' -d "$3")
	curl "${args[@]}" "$API$2"
}

# dockerx — docker хоста Detour: локальный или через ssh (на NAS docker доступен
# только через sudo по полному пути). Аргументы экранируются для удалённого sh.
dockerx() {
	if [ -n "${E2E_SSH:-}" ]; then
		ssh -o LogLevel=ERROR "$E2E_SSH" "sudo -n ${E2E_DOCKER_BIN:-/usr/local/bin/docker} $(printf '%q ' "$@")"
	else
		docker "$@"
	fi
}

ctl() { dockerx exec -e LD_LIBRARY_PATH=/opt/expressvpn/lib "$CT" /opt/expressvpn/bin/expressvpnctl "$@"; }

state() { api GET /v1/state; }

traffic_ok() { curl -s -m "${1:-5}" --socks5-hostname "$SOCKS" -o /dev/null "$PROBE_URL"; }

# ---------- монитор состояний демона ----------

# Метки времени ставятся на хосте, в тех же часах, что и опрос state.
start_monitor() {
	: >"$WORK/monitor.log"
	(
		dockerx exec -e LD_LIBRARY_PATH=/opt/expressvpn/lib "$CT" \
			stdbuf -oL /opt/expressvpn/bin/expressvpnctl monitor connectionstate 2>/dev/null |
			while IFS= read -r line; do
				line=$(printf '%s' "$line" | tr -d '\r' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
				[ -n "$line" ] && printf '%s %s\n' "$(now)" "$line" >>"$WORK/monitor.log"
			done
	) &
	MON_PID=$!
}

stop_monitor() {
	[ -n "$MON_PID" ] || return 0
	dockerx exec "$CT" pkill -f 'expressvpnctl monitor' 2>/dev/null
	kill "$MON_PID" 2>/dev/null
	MON_PID=""
}

# ---------- подготовка сценария ----------

service_ip() {
	[ -n "${E2E_COMPOSE:-}" ] || return 0
	local id
	id=$($E2E_COMPOSE ps -q "$1" 2>/dev/null)
	[ -n "$id" ] && docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$id"
}

# socks_json <a|b> → {"host":…,"port":…} из E2E_SOCKS_A/B вида host[:port].
socks_json() {
	local addr port
	case "$1" in a) addr=$SOCKS_A ;; b) addr=$SOCKS_B ;; esac
	case "$addr" in *:*) port=${addr##*:} ;; *) port=1080 ;; esac
	jq -nc --arg h "${addr%%:*}" --argjson p "$port" '{host: $h, port: $p}'
}

uplink_json() {
	case "$1" in
	a | b) jq -nc --argjson s "$(socks_json "$1")" '{mode: "socks5", socks5: ($s + {udp: "auto"})}' ;;
	host) echo '{"mode":"host"}' ;;
	esac
}

wait_connected() {
	local deadline=$(($(date +%s) + CONNECT_MAX))
	while [ "$(date +%s)" -lt "$deadline" ]; do
		if [ "$(state | jq -r .expressvpn.connection)" = connected ] && traffic_ok 5; then
			return 0
		fi
		sleep 2
	done
	return 1
}

# ensure <a|b|host> <protocol> <location>: привести конфиг и дождаться рабочего
# подключения. Это подготовка — без проверок.
ensure() {
	local patch cur
	patch=$(jq -n --argjson u "$(uplink_json "$1")" --arg p "$2" --arg l "$3" \
		'{expressvpn: {protocol: $p, location: $l}, uplink: $u}')
	cur=$(api GET /v1/config)
	if [ "$(jq -S --argjson p "$patch" '. * $p' <<<"$cur")" != "$(jq -S . <<<"$cur")" ]; then
		api PATCH /v1/config "$patch" >/dev/null
	fi
	if [ "$(state | jq -r .desired.connection)" != connected ]; then
		api POST /v1/actions/connect '{}' >/dev/null
	fi
	wait_connected || return 1
	sleep 3 # развести окна журнала и монитора соседних сценариев
}

# ---------- наблюдение и проверки ----------

# observe <t0>: state опрашивается в фоне раз в ~0,25 с (states.log), трафик —
# в основном цикле (traffic.log): curl через умирающий туннель может висеть
# секунды и не должен прятать переходы state. Конец — connected с трафиком
# (не раньше EARLY_MAX после действия) или CONNECT_MAX.
observe() {
	local t0=$1 poller tc ok el conn
	: >"$WORK/states.log"
	: >"$WORK/traffic.log"
	(
		while :; do
			ts=$(now)
			line=$(state 2>/dev/null | jq -r '[
				(.expressvpn.connection // "?"),
				(.expressvpn.connectedAt // null | if . == null then "-" else (sub("\\.[0-9]+"; "") | fromdateiso8601 | tostring) end),
				(.killswitch.active // false | tostring)
			] | join(" ")' 2>/dev/null)
			printf '%s %s\n' "$ts" "${line:-? - false}" >>"$WORK/states.log"
			sleep 0.25
		done
	) &
	poller=$!
	while :; do
		tc=$(now)
		ok=0
		traffic_ok 4 && ok=1
		printf '%s %s\n' "$tc" "$ok" >>"$WORK/traffic.log"
		conn=$(tail -1 "$WORK/states.log" | awk '{ print $2 }')
		el=$(calc "$tc - $t0")
		if [ "$ok" = 1 ] && [ "$conn" = connected ] && ! le "$el" "$EARLY_MAX"; then
			break
		fi
		le "$el" "$CONNECT_MAX" || break
		sleep 0.5
	done
	kill "$poller" 2>/dev/null
	wait "$poller" 2>/dev/null
}

FAILS=""
fail() { FAILS="${FAILS:+$FAILS; }$*"; }

# check <id> <t0> <reason> <early_max>: общие проверки; метрики — в глобальные
# переменные для таблицы.
check() {
	local id=$1 t0=$2 reason=$3 early=$4 mon=$WORK/monitor.log smp=$WORK/states.log
	local t_disc t_conn t_cing t_nc t_state bad last_ca last_ok last_conn t_traffic msgs
	FAILS=""

	# Демон: Disconnected → (Connecting) → Connected после действия.
	t_disc=$(awk -v t0="$t0" '$1 >= t0 && $2 == "Disconnected" { print $1; exit }' "$mon")
	local from=${t_disc:-$t0}
	t_conn=$(awk -v f="$from" '$1 >= f && $2 == "Connected" { print $1; exit }' "$mon")
	t_cing=$(awk -v f="$from" -v c="${t_conn:-1e12}" '$1 >= f && $1 <= c && ($2 == "Connecting" || $2 == "Reconnecting") { print $1; exit }' "$mon")
	[ -n "$t_disc" ] || fail "демон не проходил через Disconnected"
	[ -n "$t_conn" ] || fail "демон не пришёл в Connected"

	# state ушёл из connected не позже early.
	awk -v t0="$t0" -v m="$early" '($1 - t0) <= m && ($2 == "reconnecting" || $2 == "connecting") { f = 1 } END { exit !f }' "$smp" ||
		fail "state не ушёл в reconnecting|connecting за ${early} с"

	# После первого «не connected» state не должен показывать connected раньше
	# фактического Connected демона (допуск — задержка опроса).
	t_nc=$(awk '$2 != "connected" { print $1; exit }' "$smp")
	if [ -n "$t_nc" ]; then
		bad=$(awk -v s="$t_nc" -v c="${t_conn:-1e12}" '$1 > s && $2 == "connected" && $1 < c - 0.5 { print $1; exit }' "$smp")
		[ -z "$bad" ] || fail "state=connected на +$(calc "$bad - $t0") с, а демон подключился на +$([ -n "$t_conn" ] && calc "$t_conn - $t0" || echo '?') с"
	fi

	# Итог: connected + трафик, connectedAt не раньше Connected демона.
	read -r _ last_conn last_ca _ < <(tail -1 "$smp")
	read -r t_traffic last_ok < <(tail -1 "$WORK/traffic.log")
	if [ "$last_ok" = 1 ] && [ "$last_conn" = connected ]; then
		T_TRAFFIC=$(calc "$t_traffic - $t0")
	else
		T_TRAFFIC="—"
		fail "трафик через SOCKS не восстановился за ${CONNECT_MAX} с"
	fi
	if [ -n "$t_conn" ] && [ "$last_ca" != "-" ] && ! le "$(calc "$t_conn - 1.5")" "$last_ca"; then
		fail "connectedAt на $(calc "$t_conn - $last_ca") с раньше фактического подключения"
	fi

	# Справочно: подключение демона — от Disconnected (агент отдаёт `connect`
	# за сотни мс, дальше работает демон; если Disconnected не было — от
	# первого Connecting/Reconnecting) и момент connected в state.
	T_DISC=$([ -n "$t_disc" ] && calc "$t_disc - $t0" || echo "—")
	if [ -n "$t_conn" ] && [ -n "${t_disc:-$t_cing}" ]; then
		T_DAEMON=$(calc "$t_conn - ${t_disc:-$t_cing}")
	else
		T_DAEMON="—"
	fi
	t_state=""
	[ -z "$t_nc" ] || t_state=$(awk -v s="$t_nc" '$1 > s && $2 == "connected" { print $1; exit }' "$smp")
	T_STATE=$([ -n "$t_state" ] && calc "$t_state - $t0" || echo "—")

	# Kill switch всё время активен.
	awk '$4 != "true" { f = 1 } END { exit f }' "$smp" || fail "killswitch.active=false во время переключения"

	# Журнал агента.
	msgs=$(api GET "/v1/logs?component=agent&since=$(epoch2iso "$(calc "$t0 - 1")")&limit=1000" | jq -r '.entries[]?.message')
	grep -q "reason=$reason" <<<"$msgs" || fail "в журнале нет начала переподключения с reason=$reason"
	grep -q "^expressvpn connected" <<<"$msgs" || fail "в журнале нет записи о подключении"
	# Доля агента: до connected в state минус фазы демона из журнала драйвера.
	PHASES=$(api GET "/v1/logs?component=expressvpn&since=$(epoch2iso "$(calc "$t0 - 1")")&limit=200" |
		jq -r '.entries[]?.message' | grep "^session established" | tail -1 | sed 's/^session established //')
	T_AGENT="—"
	NOTE=""
	if [ -z "$PHASES" ]; then
		fail "в журнале нет записи session established с фазами"
	elif [ "$(phase attempt)" != 1 ]; then
		NOTE="подключение с попытки $(phase attempt) (сбой upstream): доля агента не проверяется"
	elif [ -n "$t_state" ]; then
		local daemon
		daemon=$(awk -v a="$(dur2s "$(phase disconnect)")" -v b="$(dur2s "$(phase connectCmd)")" \
			-v c="$(dur2s "$(phase untilConnected)")" 'BEGIN { printf "%.2f", a + b + c }')
		T_AGENT=$(calc "$t_state - $t0 - $daemon")
		le "$T_AGENT" "$AGENT_MAX" || fail "доля агента ${T_AGENT} с > ${AGENT_MAX} с"
	fi
}

record() { # record <id> <title>
	local verdict=PASS
	[ -z "$FAILS" ] || {
		verdict=FAIL
		FAILED=1
	}
	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$T_DISC" "$T_DAEMON" "$T_STATE" "$T_TRAFFIC" "$T_AGENT" "$verdict" "$FAILS${NOTE:+ ($NOTE)}" >>"$WORK/results.tsv"
	say "$1: $verdict${FAILS:+ — $FAILS}${NOTE:+ ($NOTE)}"
	[ -z "${PHASES:-}" ] || say "$1: фазы драйвера: $PHASES"
	cp "$WORK/states.log" "$WORK/$1.states.log"
	cp "$WORK/traffic.log" "$WORK/$1.traffic.log"
	awk -v t0="$T0" '$1 >= t0 - 1' "$WORK/monitor.log" >"$WORK/$1.monitor.log"
}

setup_failed() {
	printf '%s\t%s\t—\t—\t—\t—\t—\tFAIL\t%s\n' "$1" "$2" "подготовка: не удалось получить рабочее подключение" >>"$WORK/results.tsv"
	FAILED=1
	say "$1: FAIL — подготовка не удалась"
}

# ---------- сценарии ----------

T0=0
T_DISC="—"
T_DAEMON="—"
T_TRAFFIC="—"
T_STATE="—"
T_AGENT="—"
PHASES=""
NOTE=""

scenario_s1() {
	local title="socks5 A→B, lightway_tcp"
	ensure a lightway_tcp "$LOC" || {
		setup_failed s1 "$title"
		return
	}
	T0=$(now)
	api PATCH /v1/config "{\"uplink\":{\"socks5\":$(socks_json b)}}" >/dev/null
	observe "$T0"
	check s1 "$T0" uplink_changed "$EARLY_MAX"
	record s1 "$title"
}

scenario_s2() {
	local title="socks5 B→A + protocol=auto"
	local p
	ensure b lightway_tcp "$LOC" || {
		setup_failed s2 "$title"
		return
	}
	T0=$(now)
	api PATCH /v1/config "{\"expressvpn\":{\"protocol\":\"auto\"},\"uplink\":{\"socks5\":$(socks_json a)}}" >/dev/null
	observe "$T0"
	check s2 "$T0" uplink_changed "$EARLY_MAX"
	p=$(state | jq -r '[.expressvpn.protocol.effective, (.expressvpn.protocol.reason // ""), .uplink.udpSupported] | join("|")')
	[ "$p" = "auto||true" ] || fail "ожидался protocol.effective=auto без причины и udpSupported=true, получено: $p"
	record s2 "$title"
}

scenario_s3() {
	local title="reconnect action"
	ensure b lightway_tcp "$LOC" || {
		setup_failed s3 "$title"
		return
	}
	T0=$(now)
	api POST /v1/actions/reconnect '{}' >/dev/null
	observe "$T0"
	check s3 "$T0" user_request "$EARLY_MAX"
	record s3 "$title"
}

scenario_s4() {
	local title="location → $LOC_ALT"
	ensure a lightway_tcp "$LOC" || {
		setup_failed s4 "$title"
		return
	}
	T0=$(now)
	api PATCH /v1/config "{\"expressvpn\":{\"location\":\"$LOC_ALT\"}}" >/dev/null
	observe "$T0"
	check s4 "$T0" location_changed "$EARLY_MAX"
	ctl status | head -1 | grep -q "$LOC_ALT" || fail "демон подключён не к $LOC_ALT: $(ctl status | head -1)"
	record s4 "$title"
}

scenario_s5() {
	local title="protocol lightway_tcp → lightway_udp"
	ensure b lightway_tcp "$LOC" || {
		setup_failed s5 "$title"
		return
	}
	T0=$(now)
	api PATCH /v1/config '{"expressvpn":{"protocol":"lightway_udp"}}' >/dev/null
	observe "$T0"
	check s5 "$T0" protocol_changed "$EARLY_MAX"
	ctl status | grep -q "Protocol in use: LightwayUdp" || fail "демон использует не Lightway UDP: $(ctl status | grep 'Protocol in use')"
	record s5 "$title"
}

scenario_s6() {
	local title
	title="socks5 → host"
	if ensure a lightway_tcp "$LOC"; then
		T0=$(now)
		api PATCH /v1/config '{"uplink":{"mode":"host"}}' >/dev/null
		observe "$T0"
		check s6a "$T0" uplink_changed "$EARLY_MAX"
		record s6a "$title"
	else
		setup_failed s6a "$title"
	fi
	title="host → socks5"
	if ensure host lightway_tcp "$LOC"; then
		T0=$(now)
		api PATCH /v1/config "{\"uplink\":$(uplink_json a)}" >/dev/null
		observe "$T0"
		check s6b "$T0" uplink_changed "$EARLY_MAX"
		record s6b "$title"
	else
		setup_failed s6b "$title"
	fi
}

scenario_s7() {
	local title="upstream socks restart"
	[ -n "${E2E_COMPOSE:-}" ] || {
		setup_failed s7 "$title (нужен E2E_COMPOSE)"
		return
	}
	ensure a lightway_tcp "$LOC" || {
		setup_failed s7 "$title"
		return
	}
	T0=$(now)
	$E2E_COMPOSE restart -t 0 socks-a >/dev/null 2>&1
	observe "$T0"
	# Обрыв замечает демон, агент — опросом не реже раза в 5 с (спека
	# Supervision and reconnect), отсюда более мягкий порог «ухода» из connected.
	check s7 "$T0" connection_lost 5
	api GET "/v1/logs?since=$(epoch2iso "$(calc "$T0 - 1")")&limit=2000" | jq -r '.entries[]?.message' | grep -q daemon_not_ready &&
		fail "в журнале daemon_not_ready"
	record s7 "$title"
}

# ---------- запуск ----------

ORIG_CONFIG=""
ORIG_DESIRED=""

restore() {
	stop_monitor
	if [ -n "$ORIG_CONFIG" ]; then
		say "восстанавливаю конфигурацию и желаемое состояние ($ORIG_DESIRED)"
		api PATCH /v1/config "$ORIG_CONFIG" >/dev/null
		[ "$ORIG_DESIRED" = connected ] || api POST /v1/actions/disconnect '{}' >/dev/null
	fi
	if [ -s "$WORK/results.tsv" ]; then
		echo
		{
			printf 'сценарий\tчто\tDisconnected, +с\tподключение демона, с\tconnected, +с\tтрафик, +с\tдоля агента, с\tитог\tпричины\n'
			cat "$WORK/results.tsv"
		} | column -t -s $'\t'
		echo
		echo "журналы опросов и монитора: $WORK"
	fi
}

main() {
	case "${1:-}" in
	"" | -h | --help)
		usage
		exit 2
		;;
	esac
	local list=${1//,/ }
	[ "$list" = all ] && list=$ALL_SCENARIOS
	for s in $list; do
		case " $ALL_SCENARIOS " in *" $s "*) ;; *) die "неизвестный сценарий: $s" ;; esac
	done
	for bin in curl jq perl awk; do
		command -v "$bin" >/dev/null || die "нужен $bin"
	done
	[ -n "${E2E_SSH:-}" ] || command -v docker >/dev/null || die "нужен docker"


	WORK=$(mktemp -d "${TMPDIR:-/tmp}/detour-e2e.XXXXXX")
	trap restore EXIT
	trap 'exit 130' INT TERM

	say "жду агента на $API"
	local i
	for i in $(seq 1 60); do
		curl -sf -m 2 "$API/healthz" >/dev/null && break
		sleep 2
	done
	TOKEN=$(dockerx exec "$CT" cat /data/auth/token 2>/dev/null || true)
	# /healthz отвечает раньше, чем демон поднимется и прочитает сессию.
	for i in $(seq 1 45); do
		[ "$(state | jq -r .expressvpn.auth)" = logged_in ] && break
		sleep 2
	done
	[ "$(state | jq -r .expressvpn.auth)" = logged_in ] || die "ExpressVPN не в аккаунте: нужен том с выполненным входом"

	ORIG_CONFIG=$(api GET /v1/config)
	ORIG_DESIRED=$(state | jq -r .desired.connection)
	LOC=${E2E_LOCATION:-$(jq -r '.expressvpn.location // ""' <<<"$ORIG_CONFIG")}
	[ "$LOC" != "$LOC_ALT" ] || die "E2E_LOCATION совпадает с E2E_LOCATION_ALT ($LOC_ALT)"
	[ -n "$SOCKS_A" ] || SOCKS_A=$(service_ip socks-a)
	[ -n "$SOCKS_B" ] || SOCKS_B=$(service_ip socks-b)
	[ -n "$SOCKS_A" ] && [ -n "$SOCKS_B" ] || die "не нашёл адреса socks-a/socks-b: задайте E2E_COMPOSE или E2E_SOCKS_A/B"
	say "локация: ${LOC:-smart}, вторая локация (s4): $LOC_ALT; uplink'и: A=$SOCKS_A B=$SOCKS_B; сценарии: $list"

	start_monitor
	for s in $list; do
		say "=== $s"
		"scenario_$s"
	done
	exit "$FAILED"
}

main "$@"
