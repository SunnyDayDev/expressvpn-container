# Навык Detour для агентов (Claude Code, ChatGPT/Codex)

Навык [agents-plugin/skills/detour](../agents-plugin/skills/detour/SKILL.md)
учит агента управлять Detour через [API агента](api.md): статус, подключение,
смена локации и протокола, selfcheck, логи. В комплекте — скрипт
[detour.sh](../agents-plugin/skills/detour/scripts/detour.sh), которым агент
(и вы) можете пользоваться из терминала.

Каталог [agents-plugin](../agents-plugin/) — один плагин для двух экосистем:
манифест Claude Code — в `agents-plugin/.claude-plugin/plugin.json`, манифест
ChatGPT/Codex — в `agents-plugin/.codex-plugin/plugin.json`, а навык у них
общий.

## Подключение в Claude Code

Внутри этого репозитория навык работает сразу — `.claude/skills/detour` — это
symlink на `agents-plugin/skills/detour`, и Claude Code подхватывает его как
проектный навык.

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

При включении плагина Claude Code спросит адрес API и токен (`userConfig` в
манифесте плагина): значение с `sensitive: true` на macOS хранится в Keychain,
на Linux/Windows — в `~/.claude/.credentials.json` (0600), а не открытым
текстом в файле. Поменять значения можно в `/plugin` → detour; оба поля можно
оставить пустыми и настроить подключение способами из раздела
[«Настройка адреса и токена»](#настройка-адреса-и-токена).

Проверить установку: `claude plugin details detour@detour` (должно показать
`Skills (1): detour`). Маркетплейсы не автообновляются: после изменений навыка
в репозитории выполните `/plugin marketplace update detour` (или включите
автообновление в `/plugin` → Marketplaces). Установленные копии плагина
обновляются только после поднятия `version` в манифесте — правки навыка без
бампа версии до пользователей не доедут.

### Объявление маркетплейса на уровне проекта

`/plugin marketplace add` регистрирует маркетплейс в пользовательских настройках,
но его можно объявить и в настройках **другого проекта** — тогда каждый, кто
откроет тот репозиторий (после trust‑подтверждения папки), получит маркетплейс
автоматически. В `.claude/settings.json` того проекта:

```json
{
  "extraKnownMarketplaces": {
    "detour": {
      "source": { "source": "github", "repo": "SunnyDayDev/expressvpn-container" }
    }
  },
  "enabledPlugins": {
    "detour@detour": true
  }
}
```

Нюансы:

- плагины из внешних источников не ставятся автоматически: Claude Code
  зарегистрирует маркетплейс и подскажет выполнить
  `claude plugin install detour@detour`;
- `extraKnownMarketplaces` принимает только удалённые источники
  (`github`/`url`), относительный путь туда не прописать;
- в самом репозитории Detour такое объявление не нужно: навык здесь и так
  загружается как проектный из `.claude/skills/`, а установленный поверх плагин
  дублировал бы его.

Альтернатива без плагина — персональная копия или symlink на клон репозитория
(обновляется вместе с ним):

```bash
ln -s "$(pwd)/agents-plugin/skills/detour" ~/.claude/skills/detour
```

## Подключение в ChatGPT/Codex

Репозиторий объявляет и marketplace плагинов Codex
([.agents/plugins/marketplace.json](../.agents/plugins/marketplace.json)) с
локальным источником `./agents-plugin`. На машине с клоном репозитория:

```bash
codex plugin marketplace add /абсолютный/путь/к/клону/expressvpn-container
```

Проверка: `codex plugin marketplace list` должен показать marketplace `detour`.
Затем перезапустите ChatGPT desktop, откройте Plugins Directory, выберите
marketplace **Detour** и установите плагин **detour**. В локальной задаче Codex
навык вызывается как `$detour`; в обычном ChatGPT он выбирается как `@detour`,
но запуск shell-скрипта и доступ к домашней сети требуют локальной среды Codex.

После изменений навыка в репозитории выполните
`codex plugin marketplace upgrade`, при необходимости переустановите плагин и
перезапустите приложение. Не редактируйте файлы в `~/.codex/plugins/cache` —
это сгенерированная установленная копия.

`userConfig` — механизм Claude Code: Codex не передаёт настройки плагина в
окружение, поэтому для ChatGPT/Codex задайте адрес и токен способами из
следующего раздела (проще всего — файл `~/.config/detour/env`).

## Настройка адреса и токена

Навыку нужны две вещи: адрес агента и API‑токен. Токен покажет UI →
**Settings → Access**; с хоста контейнера его же можно прочитать командой
`docker exec detour-expressvpn cat /data/auth/token`.

При установке плагином проще всего ответить на вопросы при включении плагина
(см. выше): скрипт подхватывает эти значения из переменных
`CLAUDE_PLUGIN_OPTION_URL`/`CLAUDE_PLUGIN_OPTION_TOKEN`, которые выставляет
Claude Code. Способы ниже — для навыка внутри репозитория, запуска скрипта из
терминала и прочих не‑плагинных сценариев.

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
agents-plugin/skills/detour/scripts/detour.sh state
```

или просто попросите Claude: «проверь статус Detour» — навык вызовет тот же
скрипт сам (при установке плагином он лежит в
`~/.claude/plugins/cache/detour/detour/<версия>/scripts/`).

### Куда ещё можно положить токен

Приоритет источников: явные переменные окружения → настройки плагина
(`CLAUDE_PLUGIN_OPTION_*`) → файл, поэтому варианты можно совмещать (например,
файл — по умолчанию, env — для второго инстанса).

| Вариант | Как | Когда уместно |
|---|---|---|
| `userConfig` плагина | вопросы при включении плагина; поменять — `/plugin` → detour | при установке плагином: на macOS токен в Keychain, но работает только в сессиях Claude Code |
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
  agents-plugin/skills/detour/scripts/detour.sh state
```

или отдельного файла: `DETOUR_CONFIG=~/.config/detour/nas.env detour.sh state`.
