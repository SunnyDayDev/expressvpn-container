# Detour — ExpressVPN sidecar · UI design

Файл `expressvpn-sidecar-design.pen` (Pencil / pen.dev) — визуальная фиксация спек `openspec/changes/expressvpn-sidecar/specs/web-ui/spec.md`. Экспорт PNG — `design/exports/`.

Стиль — по референсу кабинета ExpressVPN+: красный `#DA3940` только как brand‑метка, интерактив зелёный `#0E7C62` с мятной подсветкой, левый сайдбар с секциями, navy‑hero с серифным заголовком (Lora), текст Inter, фон `#FBFBFD`, карточки без обводки на мягкой тени (r16); логотип ExpressVPN не используется. Структура: **Tokens** → **Atoms** → **Molecules** → экраны. UI — веб‑интерфейс, который отдаёт агент из контейнера; экраны — страницы 1280px: браузерная полоса → каркас с левым сайдбаром (Dashboard · SETTINGS · SYSTEM) → контент‑карточки; Dashboard — navy‑hero + плитки + локации + правая колонка ссылок (без карточек, как в референсе).

| # | Артборд | Спека / требование |
|---|---------|--------------------|
| 0 | Components — токены, атомы, молекулы | — |
| 9 | Web — Dashboard (connected) | web-ui: Dashboard; proxy-ingress: Connection statistics |
| 8 | Web — Diagnostics (страница верхнего уровня) | web-ui: Diagnostics in UI; agent-api: Self-check, Logs |
| 9b | Web — Dashboard (disconnected, uplink down) | web-ui: Dashboard; uplink-routing: Uplink health monitoring |
| 10 | Web Setup — Sign in (3 шага: пароль → вход → подключение) | web-ui: First-run onboarding, Admin password; expressvpn-control: Login |
| 11 | Web — Login (вход, подсказка reset-password) | web-ui: Admin password and sessions; deployment: Admin password reset |
| 5 | Web Settings — ExpressVPN | web-ui: Settings pages; expressvpn-control |
| 5b | Web Settings — Access (пароль, API‑токен, сессии, язык) | web-ui: Admin password and sessions; agent-api: Auth endpoints |
| 6 | Web Settings — Uplink | uplink-routing; web-ui: Settings pages |
| 7 | Web Settings — Proxy (порты read‑only, `.env`‑инструкции, auth) | web-ui: Recreate-scoped parameters; proxy-ingress; deployment |
| 7b | Web Settings — Container (read-only, команды compose) | deployment; container-image |

Правила: «живые» настройки применяются сразу с индикатором «Applied»; параметры уровня compose — read‑only с копируемой командой; адрес прокси на Dashboard строится из хоста адресной строки браузера.
