# Навык Detour для Claude Code

Навык [.claude/skills/detour](../.claude/skills/detour/SKILL.md) учит агента
управлять Detour через [API агента](api.md): статус, подключение, смена локации
и протокола, selfcheck, логи. В комплекте — скрипт
[detour.sh](../.claude/skills/detour/scripts/detour.sh), которым агент (и вы)
можете пользоваться из терминала.

## Подключение

Внутри этого репозитория навык работает сразу — Claude Code подхватывает
проектные навыки из `.claude/skills/`.

Чтобы управлять Detour с любой машины и из любой директории, репозиторий
опубликован как маркетплейс плагинов Claude Code (манифест —
[.claude-plugin/marketplace.json](../.claude-plugin/marketplace.json)).
В сессии Claude Code:

```
/plugin marketplace add SunnyDayDev/expressvpn-container
/plugin install detour@detour
```

то же из терминала:

```bash
claude plugin marketplace add SunnyDayDev/expressvpn-container
```

```bash
claude plugin install detour@detour
```

Проверить установку: `claude plugin details detour@detour` (должно показать
`Skills (1): detour`). Маркетплейсы не автообновляются: после изменений навыка
в репозитории выполните `/plugin marketplace update detour` (или включите
автообновление в `/plugin` → Marketplaces).

Альтернатива без плагина — персональная копия или symlink на клон репозитория
(обновляется вместе с ним):

```bash
ln -s "$(pwd)/.claude/skills/detour" ~/.claude/skills/detour
```

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

Проверка — из клона репозитория:

```bash
.claude/skills/detour/scripts/detour.sh state
```

или просто попросите Claude: «проверь статус Detour» — навык вызовет тот же
скрипт сам (при установке плагином он лежит в
`~/.claude/plugins/cache/detour/detour/<версия>/scripts/`).

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
  .claude/skills/detour/scripts/detour.sh state
```

или отдельного файла: `DETOUR_CONFIG=~/.config/detour/nas.env detour.sh state`.
