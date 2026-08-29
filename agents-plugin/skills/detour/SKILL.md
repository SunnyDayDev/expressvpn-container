---
name: detour
description: Управление Detour (ExpressVPN sidecar-контейнер) через HTTP API агента — статус VPN, подключение и отключение, смена локации и протокола, настройки uplink и защит, selfcheck, логи. Используй, когда пользователь просит проверить или переключить VPN/Detour, сменить страну или локацию ExpressVPN, продиагностировать прокси-туннель или посмотреть логи контейнера.
---

# Управление Detour через API

Detour — контейнер с ExpressVPN, который выставляет VPN как SOCKS5-прокси и
управляется HTTP/JSON API (`/v1`). Полный справочник эндпоинтов — в
[references/api.md](references/api.md) рядом с этим файлом; таблицы ниже
достаточно для типовых задач.

## Как обращаться к API

Используй скрипт [scripts/detour.sh](scripts/detour.sh) — он сам разрешает адрес
и токен (переменные `DETOUR_URL`/`DETOUR_TOKEN`/`DETOUR_TOKEN_CMD`, настройки
плагина `CLAUDE_PLUGIN_OPTION_URL`/`CLAUDE_PLUGIN_OPTION_TOKEN` или файл
`~/.config/detour/env`) и умеет ждать завершения асинхронных операций.

Путь к скрипту разрешай относительно каталога, из которого фактически загружен
этот `SKILL.md` (ниже он обозначен `<skill-dir>`), — не предполагай, что текущий
рабочий каталог совпадает с каталогом навыка:

```bash
<skill-dir>/scripts/detour.sh state                       # снимок состояния
<skill-dir>/scripts/detour.sh action connect '{"location":"de-frankfurt-1"}'
<skill-dir>/scripts/detour.sh patch '{"expressvpn":{"protocol":"lightway_tcp"}}'
<skill-dir>/scripts/detour.sh selfcheck                   # проверка туннеля
<skill-dir>/scripts/detour.sh logs uplink 100
```

Если скрипт недоступен — обычный curl с заголовком
`Authorization: Bearer $DETOUR_TOKEN` на `$DETOUR_URL` (по умолчанию
`http://127.0.0.1:48100`).

Если навык установлен как плагин Claude Code, адрес и токен задаются при
включении плагина; настроенный адрес подставлен сюда: `${user_config.url}`
(литеральный `${…}` означает, что навык загружен не как плагин Claude Code —
например, как проектный навык или в ChatGPT/Codex — бери значения из
источников выше).
Токен в текст навыка не подставляется намеренно: скрипт читает его из окружения,
в контекст и вывод токен попадать не должен.

Если и URL, и токен не настроены (`state` возвращает ошибку соединения или
`401 unauthorized`) — не подбирай значения: скажи пользователю настроить
подключение — при установке плагином Claude Code задать адрес и токен в
`/plugin` → detour, иначе по инструкции `docs/skill.md` репозитория Detour
(файл `~/.config/detour/env` с `DETOUR_URL` и `DETOUR_TOKEN`; токен виден в
UI → Settings → Access).

## Типовые задачи

- **Статус**: `state` — смотри `expressvpn.connection`, `expressvpn.location`,
  `expressvpn.publicIP`, `uplink.status`, `lastError`.
- **Подключить/отключить/переподключить**: `action connect` / `action disconnect` /
  `action reconnect`. Смена локации: `action connect '{"location":"<id>"}'`
  (id — из `locations`; `""` или id `smart` — умный выбор).
- **Сменить протокол/настройки**: `patch` c JSON merge patch. Ключи:
  `expressvpn.location|protocol|autoconnect`,
  `expressvpn.protections.{ads,trackers,malicious,adult}`, `uplink.mode`
  (`host|socks5`), `uplink.socks5.{host,port,username,password,udp}`,
  `proxy.auth` (`{username,password}` или `null`). Патч атомарный: при `400`
  ничего не применилось — читай `field`/`message`. Протоколы:
  `auto|lightway_udp|lightway_tcp|openvpn_udp|openvpn_tcp|wireguard`.
- **Проверить, что туннель работает**: `selfcheck` — вердикт `ok|warning|fail`
  с причинами и IP по обе стороны. После смены локации selfcheck подтверждает,
  что публичный IP сменился.
- **Диагностика**: `logs [component]` (`agent|expressvpn|uplink|proxy|killswitch`),
  `action probe-uplink` — если uplink `down|degraded`.

## Важные особенности

- Действия асинхронные: `POST /v1/actions/<name>` отвечает `202` с id операции;
  скрипт сам ждёт результата (`succeeded|failed` + `errorCode`). `409 conflict`
  значит, что такое же действие уже идёт — дождись его, не повторяй запрос.
- Конфигурация применяется на лету, пересоздавать контейнер не нужно.
- Пароли в `config` возвращаются как `"***"`; это значение в патче означает
  «не менять» — объект конфига можно безопасно отправлять обратно целиком.

## Безопасность

- Не печатай токен, пароли и коды активации в вывод и не пиши их в файлы
  (кроме настройки `~/.config/detour/env` по явной просьбе пользователя).
- `action login` требует код активации ExpressVPN — это секрет; предлагай
  пользователю выполнить вход через веб-интерфейс, а не передавать код в чат.
- Не вызывай `POST /v1/auth/token/rotate` и эндпоинты `auth/*` (пароль
  администратора) без явной просьбы: ротация ломает сохранённый токен у всех
  клиентов.
