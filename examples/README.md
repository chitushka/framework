# Примеры приложений Chitu

Все примеры используют только стандартную библиотеку Go и пакеты текущего модуля. Запускайте команды из корня репозитория.

**Только демонстрации, не production starters.** Basic содержит фиксированные credentials
и имя CSS-файла, Admin принимает username без пароля и теряет данные после restart,
REST использует самописный JWT и встроенный fallback secret. Проверки `httptest` не заменяют
browser E2E или security-аудит. Ограничения перечислены в [реестре гарантий](../docs/guide/guarantees.md).

## Basic

Минимальный сайт использует server-side session, `auth.Middleware`, управляемый `web.Server` и готовую CSS-сборку AdminKit из `public`.

```bash
go run ./examples/basic
```

Приложение слушает `http://localhost:8080`. Вход выполняется JSON-запросом; демонстрационные данные предназначены только для локального примера:

```bash
curl -i -c basic.cookie -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"changeme"}' \
  http://localhost:8080/login
curl -b basic.cookie http://localhost:8080/dashboard
```

## Advanced Admin

Административный пример показывает иерархию `administrator -> editor -> dictionary.view`, отдельное разрешение `dictionary.manage` и CRUD справочника. Хранилища намеренно находятся в памяти, чтобы пример запускался без внешней базы.

```bash
go run ./examples/advanced-admin
```

Сервер слушает `http://localhost:8081`. Пользователь `editor` может читать справочник, но не изменять его; `admin` имеет оба разрешения. Сначала получите cookie:

```bash
curl -i -c admin.cookie -H "Content-Type: application/json" \
  -d '{"username":"admin"}' http://localhost:8081/login
curl -b admin.cookie http://localhost:8081/admin/rbac
curl -b admin.cookie -H "Content-Type: application/json" \
  -d '{"code":"archived","name":"Archived"}' \
  http://localhost:8081/admin/dictionaries
```

Для production-приложения `MemoryStore` заменяется на `rbac.DatabaseStore`, а in-memory каталог — на репозиторий поверх `db.Connection`.

## REST API

JSON-only сервис выдаёт подписанный HS256 JWT по client credentials и защищает им группу `/v1`.
`JWT_SECRET` задаёт секрет длиной не менее 32 байт; при отсутствии переменной пример
использует встроенный демонстрационный fallback, поэтому отсутствие env не является безопасным отказом.

```bash
$env:JWT_SECRET = "replace-with-at-least-32-random-bytes"
go run ./examples/rest-api
```

Сервер слушает `http://localhost:8082`. Демонстрационный клиент — `example-client / change-me`:

```bash
curl -H "Content-Type: application/json" \
  -d '{"client_id":"example-client","client_secret":"change-me"}' \
  http://localhost:8082/v1/token
curl -H "Authorization: Bearer TOKEN" http://localhost:8082/v1/articles
```

JWT-реализация примера проверяет алгоритм, HMAC-подпись, обязательные claims и срок действия.
Для production замените её поддерживаемой библиотекой/verifier с issuer/audience/expiry
policy и ротацией ключей; удалите fallback, храните credentials вне исходного кода и включите TLS.

## Проверка

```bash
go test ./examples/...
```

Тесты покрывают сессионный вход и защищённый дашборд, различие прав editor/admin, CRUD справочника, получение JWT, доступ к API, подмену подписи и истечение токена.
