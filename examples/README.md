# Примеры приложений Chitu

Короткие исполняемые примеры без внешних сервисов находятся в `guide/http`,
`guide/jobs`, `guide/generate`. [Onboarding](../docs/guide/getting-started.md),
[worker recipe](../docs/guide/cookbook/queues.md) и
[generator quickstart](../docs/guide/generator-quickstart.md) содержат полные
source-backed snippets. `go run ./tools/checkdocs -run` проверяет их в новом модуле.

Примеры используют пакеты framework и зафиксированные зависимости. Advanced Admin
имеет отдельный Go module с SQL drivers. Запускайте команды из корня репозитория.

Basic — browser starter для одного процесса с отдельной production-конфигурацией.
Admin сохраняет users/RBAC/catalog/audit в PostgreSQL/MySQL; sessions живут в одном процессе.
REST требует внешний issuer и явно настроенный публичный ключ. Проверки `httptest` не заменяют
browser E2E или security-аудит. Ограничения перечислены в [реестре гарантий](../docs/guide/guarantees.md).

## Basic

HTML login/logout, CSRF, validation, session rotation, bounded sessions/limiter и
AdminKit через Vite manifest. Вход работает без JavaScript.

```bash
node scripts/basic.mjs start
```

Команда сама устанавливает frontend dependencies, собирает assets и Go binary.
Откройте `http://localhost:8080`, войдите как `admin / changeme` и нажмите «Выйти».
Эти credentials действуют только в development по умолчанию. Production требует
username, PHC-файл и host allowlist, использует Secure cookies за TLS proxy.
[Полная инструкция Basic](basic/README.md) содержит build, deployment config,
ограничения одного процесса и воспроизводимый Chromium E2E для HTTP и production HTTPS.

## Advanced Admin

HTML password login, users/catalog CRUD, роли/назначения и аудит. SQL RBAC проверяется
на сервере в одной транзакции с mutation/audit; editor управляет справочником,
administrator — всеми разделами. Seed требует два явно подготовленных password PHC.

```bash
node scripts/admin.mjs build
# Задайте ADMIN_DB_DRIVER/ADMIN_DB_DSN и подготовьте отдельную базу.
node scripts/admin.mjs migrate
# Задайте ADMIN_SEED_ADMIN_HASH_FILE и ADMIN_SEED_EDITOR_HASH_FILE.
node scripts/admin.mjs seed
node scripts/admin.mjs start
```

Сервер слушает `http://127.0.0.1:8081`. [Полная инструкция](advanced-admin/README.md)
содержит DSN, создание PHC, production proxy config и ограничения. Прежние JSON
login, `/admin/rbac` и `/admin/dictionaries` заменены HTML forms с CSRF и страницами
`/admin/roles`, `/admin/assignments`, `/admin/catalog`.
SQL/race и browser/restart E2E запускаются через `sh scripts/admin-e2e.sh`.

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

Сервер слушает `http://127.0.0.1:8082`. Получите TOKEN у своего issuer: alg=EdDSA,
typ=at+jwt, kid/iss/aud совпадают с конфигурацией, sub непустой, iat/exp обязательны,
lifetime не превышает 15 минут. Каждый token для выделенного audience разрешает весь
CRUD общего каталога; issuer должен ограничивать его выдачу доверенными клиентами.
Для production настройте TLS proxy и `REST_ALLOWED_HOSTS`.

```bash
curl -H "Authorization: Bearer TOKEN" http://localhost:8082/v1/articles
```

Verifier использует golang-jwt/jwt/v5 и проверяет алгоритм, signature и claims.
Ротация через `ReplaceKeys`, варианты интеграции и ограничения описаны в
[authentication contract](../docs/guide/authentication.md). Для multiple active keys
расширьте конфигурацию main до key set; этот CLI-пример загружает один публичный ключ.

[REST guide](rest-api/README.md) описывает полный CRUD, единые ошибки, pagination/filter
allowlists, bounded IP/subject quotas, OpenAPI, идемпотентность и migration прежнего demo.
Данные и квоты хранятся в памяти одного процесса и сбрасываются при restart.
Contract/process E2E: `go test -race -tags e2e ./examples/rest-api` на Linux;
на Windows без C compiler уберите `-race`. OpenAPI доступен через `/openapi.json`.

## Проверка

```bash
go test ./examples/...
go -C examples/advanced-admin test ./...
```

Автономные tests проверяют Basic/REST HTTP и Admin config/templates. SQL Admin tests
с реальными drivers и browser сценарии выполняются отдельно обязательным E2E helper.
