# Примеры приложений Chitu

Все примеры используют только стандартную библиотеку Go и пакеты текущего модуля. Запускайте команды из корня репозитория.

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

JSON-only сервис выдаёт подписанный HS256 JWT по client credentials и защищает им группу `/v1`. Секрет берётся из `JWT_SECRET` и должен содержать не менее 32 байт.

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

JWT-реализация примера проверяет алгоритм, HMAC-подпись, обязательные claims и срок действия. Для production следует хранить client credentials и секреты вне исходного кода, включить TLS и настроить ротацию ключей.

## Проверка

```bash
go test ./examples/...
```

Тесты покрывают сессионный вход и защищённый дашборд, различие прав editor/admin, CRUD справочника, получение JWT, доступ к API, подмену подписи и истечение токена.
