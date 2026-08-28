# Навык Detour для Claude Code

Навык [.claude/skills/detour](../.claude/skills/detour/SKILL.md) учит агента
управлять Detour через [API агента](api.md): статус, подключение, смена локации
и протокола, selfcheck, логи. В комплекте — скрипт
[detour.sh](../.claude/skills/detour/scripts/detour.sh), которым агент (и вы)
можете пользоваться из терминала.

## Подключение

Внутри этого репозитория навык работает сразу — Claude Code подхватывает
проектные навыки из `.claude/skills/`. Чтобы управлять Detour из любой другой
директории, установите навык как персональный:

```bash
cp -R .claude/skills/detour ~/.claude/skills/detour
```

(или symlink на клон репозитория, чтобы навык обновлялся вместе с ним:
`ln -s "$(pwd)/.claude/skills/detour" ~/.claude/skills/detour`).

## Настройка адреса и токена

Навыку нужны две вещи: адрес агента и API‑токен. Токен покажет UI →
**Settings → Access**; с хоста контейнера его же можно прочитать командой
`docker exec detour-expressvpn cat /data/auth/token`.

Рекомендуемый способ — файл `~/.config/detour/env` (скрипт читает его сам):

```bash
mkdir -p ~/.config/detour
cat > ~/.config/detour/env <<'EOF'
DETOUR_URL=http://127.0.0.1:48100
DETOUR_TOKEN=dtr_…
EOF
chmod 600 ~/.config/detour/env
```

Проверка:

```bash
~/.claude/skills/detour/scripts/detour.sh state
```

### Куда ещё можно положить токен

Явные переменные окружения всегда имеют приоритет над файлом, поэтому варианты
можно совмещать (например, файл — по умолчанию, env — для второго инстанса).

| Вариант | Как | Когда уместно |
|---|---|---|
| Файл `~/.config/detour/env` (0600) | см. выше | по умолчанию: работает для любых скриптов, токен не светится в окружении каждого процесса |
| macOS Keychain | `security add-generic-password -s detour -a token -w 'dtr_…'`, в файле указать только `DETOUR_TOKEN_CMD="security find-generic-password -s detour -w"` | токен вообще не лежит на диске открытым текстом; macOS попросит разрешение при первом чтении |
| env Claude Code | блок `"env": {"DETOUR_URL": "…", "DETOUR_TOKEN": "dtr_…"}` в `~/.claude/settings.json` (или в `.claude/settings.local.json` проекта — он в gitignore) | если Detour нужен только агенту, а не другим скриптам |
| Профиль шелла (`~/.zshrc`) | `export DETOUR_TOKEN=…` | самый простой, но токен попадает в окружение всех процессов и в бэкапы дотфайлов — не рекомендуется |

Чего делать не надо: коммитить токен в репозиторий (в т.ч. в `.env` проекта —
он для параметров compose) и передавать его в URL‑параметрах.

При ротации токена (UI → Settings → Access → Rotate) обновите значение в
выбранном хранилище — старый токен перестаёт действовать сразу.

## Несколько инстансов

Скрипт берёт `DETOUR_URL`/`DETOUR_TOKEN` из окружения поверх файла, поэтому для
второго инстанса (например, NAS) достаточно:

```bash
DETOUR_URL=https://expressvpn.home DETOUR_TOKEN=dtr_… \
  ~/.claude/skills/detour/scripts/detour.sh state
```

или отдельного файла: `DETOUR_CONFIG=~/.config/detour/nas.env detour.sh state`.
