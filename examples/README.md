# Примеры приложений Chitu

Примеры используют пакеты текущего Go-модуля и его зафиксированные зависимости. Запускайте команды из корня репозитория.

**Только демонстрации, не production starters.** Basic содержит фиксированные credentials
и имя CSS-файла, Admin принимает username без пароля и теряет данные после restart,
REST требует внешний issuer и явно настроенный публичный ключ. Проверки `httptest` не заменяют
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

JSON-only сервис защищает группу `/v1` внешними Ed25519 access tokens через `auth.JWTVerifier`.
Он не выдаёт токены: `/v1/token`, встроенные client credentials и JWT_SECRET/fallback удалены.
Обязательны публичный PEM-ключ, его kid, issuer и audience. Отсутствующая/неверная конфигурация
останавливает запуск; signing key хранится у внешнего issuer, не у API.

```powershell
$env:JWT_PUBLIC_KEY_FILE = "C:/keys/issuer-ed25519-public.pem"
$env:JWT_KEY_ID = "current-key-id"
$env:JWT_ISSUER = "https://identity.example"
$env:JWT_AUDIENCE = "articles-api"
go run ./examples/rest-api
```

Сервер слушает `http://localhost:8082`. Получите TOKEN у своего issuer: alg=EdDSA,
typ=at+jwt, kid/iss/aud совпадают с конфигурацией, sub непустой, iat/exp обязательны,
lifetime не превышает 15 минут. Для production включите TLS и application authorization.

```bash
curl -H "Authorization: Bearer TOKEN" http://localhost:8082/v1/articles
```

Verifier использует golang-jwt/jwt/v5 и проверяет алгоритм, signature и claims.
Ротация через `ReplaceKeys`, варианты интеграции и ограничения описаны в
[authentication contract](../docs/guide/authentication.md). Для multiple active keys
расширьте конфигурацию main до key set; этот CLI-пример загружает один публичный ключ.

## Проверка

```bash
go test ./examples/...
```

Тесты покрывают сессионный вход и защищённый дашборд, различие прав editor/admin, CRUD справочника, проверку внешнего JWT, доступ к API, подмену подписи, истечение токена и отзыв ключа.
