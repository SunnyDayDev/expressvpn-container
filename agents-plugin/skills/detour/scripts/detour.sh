#!/usr/bin/env bash
# detour.sh — CLI-обёртка над API агента Detour (docs/api.md).
#
# Подключение (в порядке приоритета):
#   1) переменные окружения DETOUR_URL, DETOUR_TOKEN (или DETOUR_TOKEN_CMD);
#   2) настройки плагина Claude Code — CLAUDE_PLUGIN_OPTION_URL и
#      CLAUDE_PLUGIN_OPTION_TOKEN из userConfig плагина detour (задаются при
#      включении плагина; вне сессии Claude Code этих переменных нет);
#   3) файл ${XDG_CONFIG_HOME:-~/.config}/detour/env (chmod 600) с теми же
#      переменными; путь можно переопределить через DETOUR_CONFIG.
# DETOUR_TOKEN_CMD — команда, печатающая токен (например, macOS Keychain:
#   security find-generic-password -s detour -w). Используется, если
# DETOUR_TOKEN не задан. Если токена нет вовсе, запросы идут без него
# (сработает только при выключенной защите паролем).
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: detour.sh <command> [args]

  health                       GET /healthz
  state                        GET /v1/state
  config                       GET /v1/config
  patch '<json>'               PATCH /v1/config (JSON merge patch)
  action <name> ['<json>']     POST /v1/actions/<name>, ждёт завершения операции
  op <id>                      GET /v1/operations/<id>
  locations                    GET /v1/locations
  logs [component] [limit]     GET /v1/logs (component: agent|expressvpn|uplink|proxy|killswitch)
  version                      GET /v1/version
  selfcheck                    action selfcheck + печатает state.selfcheck
  get <path>                   произвольный GET (путь от корня, например /v1/state)
  post <path> ['<json>']       произвольный POST

Действия: connect [{"location":"id"}], disconnect, reconnect, login
({"activationCode":"…"}), logout, refresh-locations, selfcheck, probe-uplink.
EOF
  exit 1
}

# --- разрешение URL и токена: явный env поверх настроек плагина и конфиг-файла ---
env_url="${DETOUR_URL:-}"
env_token="${DETOUR_TOKEN:-}"
env_token_cmd="${DETOUR_TOKEN_CMD:-}"
plugin_url="${CLAUDE_PLUGIN_OPTION_URL:-}"
plugin_token="${CLAUDE_PLUGIN_OPTION_TOKEN:-}"
config_file="${DETOUR_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/detour/env}"
if [ -f "$config_file" ]; then
  # shellcheck disable=SC1090
  . "$config_file"
fi
DETOUR_URL="${env_url:-${plugin_url:-${DETOUR_URL:-http://127.0.0.1:48100}}}"
DETOUR_URL="${DETOUR_URL%/}" # хвостовой слэш дал бы //v1/… и редирект от mux
DETOUR_TOKEN="${env_token:-${plugin_token:-${DETOUR_TOKEN:-}}}"
DETOUR_TOKEN_CMD="${env_token_cmd:-${DETOUR_TOKEN_CMD:-}}"
if [ -z "$DETOUR_TOKEN" ] && [ -n "$DETOUR_TOKEN_CMD" ]; then
  DETOUR_TOKEN="$(eval "$DETOUR_TOKEN_CMD")"
fi

auth_args=()
if [ -n "$DETOUR_TOKEN" ]; then
  auth_args=(-H "Authorization: Bearer $DETOUR_TOKEN")
fi
# ${auth_args[@]+…} — bash 3.2 (macOS) с set -u падает на пустом "${arr[@]}"

req() { # method path [json-body]
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS --fail-with-body -X "$method" ${auth_args[@]+"${auth_args[@]}"})
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  curl "${args[@]}" "$DETOUR_URL$path"
  echo
}

json_field() { # <field> — печатает поле из JSON на stdin (пусто, если нет)
  python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1], ""))' "$1"
}

wait_op() { # <op-id> — ждёт завершения, печатает операцию; exit 1 при failed
  local id="$1" op status
  for _ in $(seq 1 120); do
    op="$(curl -sS --fail-with-body ${auth_args[@]+"${auth_args[@]}"} "$DETOUR_URL/v1/operations/$id")"
    status="$(printf '%s' "$op" | json_field status)"
    if [ "$status" != "running" ]; then
      printf '%s\n' "$op"
      [ "$status" = "succeeded" ]
      return
    fi
    sleep 1
  done
  echo "timeout waiting for operation $id" >&2
  return 1
}

cmd="${1:-}"
[ -n "$cmd" ] || usage
shift

case "$cmd" in
health)    req GET /healthz ;;
state)     req GET /v1/state ;;
config)    req GET /v1/config ;;
patch)     [ $# -ge 1 ] || usage; req PATCH /v1/config "$1" ;;
op)        [ $# -ge 1 ] || usage; req GET "/v1/operations/$1" ;;
locations) req GET /v1/locations ;;
version)   req GET /v1/version ;;
logs)
  path="/v1/logs?limit=${2:-100}"
  [ -n "${1:-}" ] && path="$path&component=$1"
  req GET "$path"
  ;;
action)
  [ $# -ge 1 ] || usage
  name="$1"; body="${2:-}"
  resp="$(req POST "/v1/actions/$name" "$body")"
  id="$(printf '%s' "$resp" | json_field operation)"
  if [ -z "$id" ]; then # 409 и прочее — печатаем ответ как есть
    printf '%s\n' "$resp"
    exit 1
  fi
  wait_op "$id"
  ;;
selfcheck)
  resp="$(req POST /v1/actions/selfcheck)"
  id="$(printf '%s' "$resp" | json_field operation)"
  [ -n "$id" ] || { printf '%s\n' "$resp"; exit 1; }
  wait_op "$id" >/dev/null || true
  req GET /v1/state | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin).get("selfcheck"), indent=2, ensure_ascii=False))'
  ;;
get)       [ $# -ge 1 ] || usage; req GET "$1" ;;
post)      [ $# -ge 1 ] || usage; req POST "$1" "${2:-}" ;;
*)         usage ;;
esac
