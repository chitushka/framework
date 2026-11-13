# REST starter

JSON API с внешними Ed25519 access tokens, полным CRUD статей, ограниченными
ресурсами и [OpenAPI 3.1](openapi.json). Спецификация встроена в binary и доступна
без токена через `GET /openapi.json`.

## Запуск из checkout

Нужен Go из корневого `go.mod`. Команды выполняются из корня репозитория;
frontend, SQL и другие сервисы для этого starter не требуются.

```powershell
$env:JWT_PUBLIC_KEY_FILE = "C:/keys/issuer-ed25519-public.pem"
$env:JWT_KEY_ID = "current-key-id"
$env:JWT_ISSUER = "https://identity.example"
$env:JWT_AUDIENCE = "articles-api"
go run ./examples/rest-api
```

На Linux задайте те же переменные через `export`. Сервер слушает
`http://127.0.0.1:8082`. Для отдельного binary:

```bash
go build -o .cache/rest-api ./examples/rest-api
```

Signing key находится только у issuer. API читает публичный PEM (PKIX, Ed25519,
до 16 KiB) и не выдаёт токены. Нет `/v1/token`, client credentials, HMAC secret
или fallback. Ошибка ключа, kid, issuer, audience или конфигурации останавливает
запуск. Получите access token у своего issuer: `alg=EdDSA`, `typ=at+jwt`,
`kid/iss/aud` совпадают с настройками, `sub` непустой, `iat/exp` обязательны,
срок жизни не более 15 минут, leeway отсутствует.

**Модель доступа:** каждый корректный token для выделенного audience разрешает
весь CRUD общего каталога. Issuer должен выдавать этот audience только доверенным
клиентам с правом записи. Per-user ownership, scopes и отдельные read-only роли
не реализованы; добавьте application authorization перед использованием с иной
моделью доступа. JWT subject служит ключом квоты, не границей доступа к данным.

## HTTP-контракт

`Authorization: Bearer TOKEN` обязателен для matched CRUD routes, включая HEAD.
Query/cookie tokens и несколько Authorization headers отвергаются. Успех:
`{"data":...}`; ошибки всех application HTTP-слоёв, включая proxy/router/limits:

```json
{"error":{"status":422,"code":"validation_failed","message":"Unprocessable Entity"}}
```

`status` совпадает с HTTP status; `message` — стандартное HTTP status text без
internal details. Используйте `code` и status, а не текст сообщения. Ошибки и
авторизованные ответы имеют `Cache-Control: no-store`. Отказы самого HTTP server
(например, слишком большие headers, timeout/оборванное соединение) могут иметь
стандартный transport response без JSON. HEAD не возвращает тело. OPTIONS
известного ресурса возвращает 204 и Allow без токена; неизвестный route — 404,
неподдерживаемый method — 405 с Allow. CORS не включён.

| Запрос | Результат |
| --- | --- |
| `GET /v1/articles` | 200, `data: []`, `meta: {limit, offset, total}` |
| `GET /v1/articles/{id}` | 200 или 404 |
| `POST /v1/articles` | 201, `data`, root-relative `Location` |
| `PUT /v1/articles/{id}` | 200, полная замена существующей записи; без upsert |
| `DELETE /v1/articles/{id}` | 204 без тела; повтор — 404 |

POST/PUT принимают только `application/json` или `application/*+json` и объект
`{"title":"..."}`. Title обрезается по краям, обязателен, не может быть пустым,
максимум 200 UTF-8 bytes после trim. Неизвестные поля (включая `id`), неверные
типы и несколько JSON values дают 400; отсутствующий/null/пустой/длинный title —
422. Неверный media type — 415. Body ограничен 4096 bytes, включая chunked
requests (413). `Content-Encoding` не поддерживается (400).

Список принимает только следующие query keys, каждый не более одного раза:

| Ключ | Значения |
| --- | --- |
| `limit` | 1–100, по умолчанию 20 |
| `offset` | 0–1000000, по умолчанию 0 |
| `title` | Case-insensitive substring, до 200 UTF-8 bytes |
| `sort` | `id`, `-id`, `title`, `-title`; по умолчанию `id` |

Title сортируется лексикографически по UTF-8; ID разрешает совпадения, направление
одинаково для обоих полей. Total — число записей после фильтрации до пагинации.
Пустая страница возвращает `[]`. Неизвестные/повторные/некорректные query keys,
query на остальных CRUD routes и raw query более 2048 bytes дают 400.
Каждая страница — отдельный snapshot: конкурентные изменения могут сдвигать offset.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  'http://127.0.0.1:8082/v1/articles?limit=10&sort=-id&title=chitu'
curl -i -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"My article"}' http://127.0.0.1:8082/v1/articles
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Updated"}' http://127.0.0.1:8082/v1/articles/2
curl -i -X DELETE -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8082/v1/articles/2
```

## Повторные запросы и совместимость

GET/HEAD не меняют статьи, но расходуют квоту. PUT идемпотентен по состоянию:
повтор одного title оставляет ту же запись. Конкурентные записи сериализуются,
действует last-writer-wins; conditional updates/ETag и защита от lost update не
обещаются. DELETE идемпотентен по состоянию, его повтор возвращает 404. POST
неидемпотентен: каждый успешный запрос создаёт новый ID. После потери ответа не
повторяйте POST автоматически. Durable deduplication отсутствует;
непустой `Idempotency-Key` явно отвергается с 400.

Пути, success statuses, `data`, error `status/code` и правила параметров фиксируются
как контракт `/v1`. Клиенты должны игнорировать новые response fields и принимать
новые error codes с fallback по status. Breaking changes требуют `/v2` и migration
guide. Это первое определение REST starter v1; прежний demo возвращал весь список,
теперь по умолчанию только 20 записей, добавлена `meta`, error messages стали
generic, `error.code` добавлен. POST больше не принимает `id` и ограничивает title.
Пользователям demo нужно перейти на пагинацию и input только с title.

## Конфигурация и ограничения

| Переменная | Default / контракт |
| --- | --- |
| `REST_ADDRESS` | `127.0.0.1:8082`, host:port |
| `APP_ENV` | пусто/development или production; HTTP production wrapper включён всегда |
| `REST_ALLOWED_HOSTS` | CSV точных authorities; development: localhost/127.0.0.1/[::1] с configured port; production: обязательно явно |
| `REST_TRUSTED_PROXIES` | CSV CIDR; пусто = не доверять forwarded headers; иначе X-Forwarded policy |
| `REST_CLIENT_RATE` | 120/IP/minute; целое 1–100000 |
| `REST_SUBJECT_RATE` | 60/verified subject/minute; целое 1–100000 |
| `REST_MAX_ARTICLES` | 10000; целое 1–100000, включая начальную Welcome-запись |

Production размещается за TLS reverse proxy: укажите внешнюю authority (с портом,
если он нестандартный), только CIDR настоящего proxy; proxy должен заменять
forwarded headers. Direct headers от остальных peers не влияют на IP/host/scheme.
При bind на `0.0.0.0` закройте прямой внешний доступ firewall. Профиль отключает
framework diagnostics и скрывает internal errors. TLS и edge limits настраивает
deployment; server использует конечные framework header/read/write/idle timeouts
и bounded shutdown. [Контракт proxy](../../docs/guide/security-audit.md).

IP quota проверяется после proxy boundary до JWT и router parsing, включая
documentation, неизвестные routes и неавторизованные запросы. Ранние отказы
server resource layer (включая проверку Content-Type и form parsing), proxy и
запрещённых debug paths могут происходить до квот; server body limit также 4096
bytes. JSON-контракт оборачивает и этот внешний server layer. Subject
quota проверяется после JWT на matched CRUD routes. Fixed window длится минуту
от первого принятого запроса. Каждая таблица содержит до 4096 активных hashed keys;
при исчерпании квоты/таблицы — 429 с целым `Retry-After` (секунды). Непринятый по
subject запрос всё равно расходует IP quota. Количество IP нельзя подменить
непроверенным X-Forwarded-For; общий NAT делит IP quota.

Данные и квоты process-local: restart сбрасывает их и создаёт Welcome-запись с ID=1.
ID не переиспользуются в пределах процесса; при исчерпании ёмкости — 503 без
mutation. Нет disk persistence, backup, shared limiter или гарантий нескольких
replicas. Для них нужны application SQL/storage и shared limiter adapter.
Основной JWT adapter поддерживает атомарную `ReplaceKeys`, но этот CLI читает
один ключ только на старте: rotation через обновление PEM/kid и restart завершает
in-memory catalog. Для непрерывной ротации расширьте key-set configuration и
подключите persistent storage. [JWT contract](../../docs/guide/authentication.md).

## Проверка

```bash
go vet ./...
go test ./examples/rest-api
# Linux с CGO/race detector: contract + concurrency + отдельный процесс по TCP.
go test -race -tags e2e -count=1 ./examples/rest-api
# Windows без C compiler:
go test -tags e2e -count=1 ./examples/rest-api
```

E2E сам создаёт внешний Ed25519 issuer key/token, собирает binary во временный
каталог, проверяет production entry point, auth, CRUD, ошибки, квоту и shutdown
(SIGTERM на Linux; Windows завершает процесс). Handler tests дополнительно
проверяют JSON Schema keywords используемой OpenAPI-спецификации на реальных
responses, allowlists, chunked oversize, Host/proxy policy и race admission.
Отдельный CI job сохраняет JSON test report. Локальный отчёт и границы проверок —
[приёмка 11.3](../../docs/stage-11-3-acceptance.md).
