## MODIFIED Requirements

### Requirement: Actions
Агент SHALL предоставлять `POST /v1/actions/<name>` для: `connect` (опционально `{location}`), `disconnect`, `reconnect`, `login` (`{activationCode}`), `logout`, `refresh-locations`, `selfcheck`, `probe-uplink`. Действие SHALL выполняться асинхронно: ответ `202` с `{ "operation": "<id>" }`; прогресс и результат SHALL быть доступны через `GET /v1/operations/<id>` и в событиях. Конфликтующее действие над тем же ресурсом во время выполнения SHALL получать `409`.

`reconnect` SHALL устанавливать новую сессию демона (отключение, затем подключение), даже если демон считает текущую сессию активной. Операция SHALL завершаться `succeeded` только после фактического подключения демона в новой сессии. `disconnect` SHALL завершаться `succeeded` только после того, как демон пришёл в `Disconnected`.

#### Scenario: Login with activation code
- **WHEN** клиент вызывает `login` с корректным кодом активации
- **THEN** операция завершается `succeeded`, `state.expressvpn.auth = logged_in`, код активации не встречается в логах агента

#### Scenario: Login with wrong code
- **WHEN** клиент вызывает `login` с неверным кодом
- **THEN** операция завершается `failed` с кодом `invalid_activation_code` и человекочитаемым сообщением

#### Scenario: Concurrent connect
- **WHEN** операция `connect` выполняется и приходит второй `connect`
- **THEN** второй запрос получает `409` с идентификатором текущей операции

#### Scenario: Reconnect with a session the daemon considers alive
- **WHEN** VPN подключён, туннель фактически не работает, но демон ещё отвечает `Connected`, и клиент вызывает `reconnect`
- **THEN** демон проходит через `Disconnected` и подключается заново, операция завершается `succeeded` только после этого, `connectedAt` соответствует новому подключению
