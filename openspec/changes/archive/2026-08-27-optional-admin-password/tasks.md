# Tasks: optional-admin-password

## 1. Агент: состояние защиты

- [x] 1.1 В `internal/api/sessions.go` добавить маркер `<dataDir>/auth/disabled`: методы `AuthDisabled()`, `Skip()` (создаёт маркер, только из `unset`), `Disable()` (проверяет текущий пароль под общим троттлингом, удаляет файл пароля, ставит маркер, сбрасывает сессии); `SetPassword` удаляет маркер; конфликт «оба файла» разрешается в пользу пароля. Проверка: unit-тесты переходов unset/set/disabled в `auth_test.go` проходят (`go test ./internal/api/`).
- [x] 1.2 `ResetPassword` (CLI `reset-password`) удаляет и файл пароля, и маркер → состояние `unset`. Проверка: unit-тест.

## 2. Агент: API

- [x] 2.1 В `auth_handlers.go`: `handleAuthStatus` возвращает `authDisabled` (и `authenticated: true` при выключенной защите); новые обработчики `POST /v1/auth/skip` (`409` вне `unset`) и `POST /v1/auth/disable` (`401` при неверном пароле, `429` под троттлингом); `handleAuthSetup` работает также из `disabled` и включает защиту. Роуты в `server.go`. Проверка: httptest-тесты всех переходов и кодов ответов из спеки agent-api.
- [x] 2.2 В `server.go` `authorized()` возвращает true при `disabled`; для мутирующих методов при `disabled` добавить Origin-щит: `403 cross_site_blocked`, если `Origin` не совпадает с `Host` запроса или `Sec-Fetch-Site: cross-site`; запросы без этих заголовков проходят. Проверка: httptest-тесты — curl-подобный PATCH проходит, cross-site PATCH получает `403`, same-origin проходит, GET не ограничивается.

## 3. Web UI

- [x] 3.1 `lib/api.js`: методы `skip()`, `disableAuth(current)`; `App.svelte`: при `authDisabled` фаза `app` без login/setup, кнопка «Sign out» скрыта. Проверка: `npm run build` и ручной прогон — UI на «выключенном» агенте открывается сразу.
- [x] 3.2 `Setup.svelte`: на шаге 1 ссылка «Continue without password» с предупреждением (артборд 10b); по клику — `skip()` и переход к шагу 2. Проверка: онбординг на свежем томе проходит без создания пароля.
- [x] 3.3 `Access.svelte`: при включённой защите — кнопка «Turn off…» с подтверждением текущим паролем (артборд 5b); при выключенной — warn-баннер, форма «Set password…», скрытая строка Sessions, пометка у API-токена (артборд 5c). Проверка: оба состояния переключаются без перезагрузки страницы.
- [x] 3.4 `lib/i18n.js`: строки en/ru для новых элементов (skip, предупреждения, баннер, turn off/set password). Проверка: переключение языка в Access не оставляет непереведённых ключей.

## 4. Спеки и проверка целиком

- [x] 4.1 `openspec validate optional-admin-password --strict` проходит; `go vet ./...` и `go test ./...` в `image/agent`, сборка web — зелёные (как в CI).
- [x] 4.2 Сквозная проверка на чистом томе: skip в онбординге → всё работает без входа; Set password → login появляется; Turn off → снова без входа; `detour-agent reset-password` → снова setup. Проверка: чек-лист сценариев из дельта-спек выполнен вручную через `docker compose up`.
