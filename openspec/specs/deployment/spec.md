## Purpose

Развёртывание и жизненный цикл контейнера без нативного приложения: docker compose как единственный инструмент установки, обновления и пересоздания — одинаково на Mac и NAS.

## Requirements

### Requirement: Compose-based bootstrap
Репозиторий SHALL содержать `docker-compose.yml` и `.env.example` (порты SOCKS5 и HTTP UI/API, адрес привязки, версия ExpressVPN‑инсталлятора, версия sing-box). Из чистого клона SHALL работать последовательность: скопировать `.env.example` в `.env` → `docker compose up -d --build` → открыть `http://<bind>:<httpPort>/` и пройти онбординг. Compose SHALL задавать все обязательные параметры запуска (capabilities, `/dev/net/tun`, том, healthcheck, `restart: unless-stopped`, отключение IPv6) — пользователю не нужно ничего дописывать.

#### Scenario: Clean start on Apple Silicon
- **WHEN** на Mac arm64 с Docker выполнено `docker compose up -d --build` из чистого клона
- **THEN** контейнер собирается нативно (arm64), становится `healthy`, UI отвечает на `http://127.0.0.1:48100/`

#### Scenario: Missing tun on host
- **WHEN** compose запущен на хосте без `/dev/net/tun`
- **THEN** контейнер завершается с понятной ошибкой агента (код 78), видимой в `docker compose logs`

### Requirement: Recreate-scoped changes via .env
Смена портов на хосте, адреса привязки или версий компонентов SHALL выполняться правкой `.env` и `docker compose up -d --build`. Состояние (вход в ExpressVPN, конфигурация агента, пароль UI) SHALL переживать пересоздание за счёт именованного тома.

#### Scenario: Change SOCKS5 host port
- **WHEN** пользователь меняет `SOCKS_PORT` в `.env` с 1080 на 1081 и выполняет `docker compose up -d`
- **THEN** прокси доступен на новом порту, вход в ExpressVPN и настройки сохранены

### Requirement: Admin password reset from the host
Агент SHALL предоставлять команду сброса пароля администратора, выполняемую с хоста: `docker compose exec detour detour-agent reset-password` — она удаляет хэш пароля и все сессии, после чего первый визит в UI снова предлагает создать пароль. Страница входа SHALL подсказывать этот путь.

#### Scenario: Forgotten password
- **WHEN** пользователь выполняет команду сброса и открывает UI
- **THEN** активные сессии недействительны, UI показывает страницу создания пароля

### Requirement: Quickstart documentation
`README.md` SHALL документировать: требования (Docker с compose), быстрый старт, адреса UI и прокси, обновление версии ExpressVPN, оговорку о лицензии (образ не публикуется, инсталлятор скачивается с сайта ExpressVPN при сборке) и раздел про NAS: привязка к интерфейсу, обязательность пароля UI и auth у SOCKS5 вне loopback, отсутствие `host.docker.internal` на Linux (использовать IP docker‑шлюза или общую сеть с xray).

#### Scenario: NAS deployment by README
- **WHEN** пользователь разворачивает compose на Linux‑NAS по разделу README
- **THEN** UI доступен по адресу NAS, вход требует пароль, SOCKS5 требует логин/пароль, прокси‑адрес в dashboard показывает адрес NAS
