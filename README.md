# Chitu Framework

Chitu — модульный веб-фреймворк с module path `github.com/chitushka/framework`. Архитектура и удобство разработки опираются на Yii2, а реализация следует идиоматическому Go без буквального переноса динамических PHP-механизмов. Фреймворк включает web-слой, доступ к данным, прикладные подсистемы, генераторы и production-инструменты.

Последовательное руководство по проектированию, разработке, безопасности и эксплуатации находится в [Chitu Definitive Guide](docs/guide/README.md).

[Onboarding](docs/guide/getting-started.md) запускает первый HTTP-сервис без Node/SQL.
[Cookbook](docs/guide/cookbook/README.md), [generator quickstart](docs/guide/generator-quickstart.md)
и [API migration guide](docs/guide/api-migration.md) содержат рабочие рецепты.
Проверка документации и примеров в отдельном Go-модуле:
`go mod download`, затем `go run ./tools/checkdocs -run`.

Production-профиль, secret redaction и файловые ответы с защитой от traversal:
[HTTP/tooling security audit и checklist](docs/guide/security-audit.md).

Проект до v1.0: production-ready статус ещё не подтверждён. Область проверенной совместимости
задаёт [контракт поддержки](docs/guide/support.md); доказательства и ограничения API собраны
в [реестре гарантий](docs/guide/guarantees.md). REQUIREMENTS описывает целевые требования.

## Готовые примеры приложений

В каталоге [`examples`](examples/README.md) находятся три запускаемых приложения:

- `basic` — browser login/logout с CSRF, validation, Vite manifest и production config;
- `advanced-admin` — SQL users/RBAC/catalog, формы CRUD, назначения ролей и аудит;
- `rest-api` — полный articles CRUD с внешним Bearer JWT, pagination, rate limits и OpenAPI.

Каждый пример отделяет сборку HTTP handler от `main`, поэтому пользовательские сценарии проверяются через `httptest`. Команды запуска, демонстрационные учётные данные и запросы приведены в [руководстве по примерам](examples/README.md).

Basic собирается и запускается одной командой `node scripts/basic.mjs start`;
[инструкция starter](examples/basic/README.md) описывает browser E2E и production за TLS proxy.
Development defaults `admin / changeme` отсутствуют в production без явного PHC-файла.
Advanced Admin использует PostgreSQL/MySQL, password login и transactional authorization/audit.
[Инструкция Admin](examples/advanced-admin/README.md) описывает отдельные build/migrate/seed,
production config, browser/restart E2E и ограничения sessions одного процесса.
REST [starter guide](examples/rest-api/README.md) описывает JSON errors, allowlists,
IP/subject quotas, OpenAPI и process E2E; каталог и квоты process-local.
REST проверяет внешние Ed25519 tokens через пакет auth и требует явной
конфигурации ключа/issuer/audience. [Reusable authentication](docs/guide/authentication.md)
предоставляет Argon2id, credentials/throttling и JWT verifier; user persistence и identity provider
подключает приложение.

## Проверка через Docker

Локальная установка Go не требуется:

```bash
docker build --target test -t chitu-test .
```

Обязательный интеграционный baseline с настоящими PostgreSQL, MySQL, Redis, Memcached и RabbitMQ:

```bash
sh scripts/integration.sh
```

В PowerShell: `./scripts/integration.ps1`. Нужен Docker Compose v2 с Linux containers.
Команда собирает runner, ждёт health checks и удаляет тестовые containers/volumes после
завершения. Порты на host не публикуются. Ошибка или недоступность сервиса завершают проверку
с ненулевым кодом. Стенд проверяет SQL/AR/generator conformance, migration/queue crash recovery,
TLS cache и готовые Redis/RabbitMQ adapters. [Контракты этапов 8–9](docs/guide/README.md).

Для интерактивной разработки:

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26 go test ./...
```

## Качество

[Quality gate contract](docs/guide/quality-gates.md) описывает обязательную OS/Go
matrix, lint всех modules, security/secret scans, negative coverage checks,
fuzz smoke и scheduled runs, artifacts, SHA pins и Dependabot. [Приёмка 12.1](docs/stage-12-1-acceptance.md)
отделяет локальные результаты от ещё не выполненных hosted checks.

Локальные проверки собраны в `Makefile`: `make test`, `make vet`, `make lint`,
`make coverage` и `make fuzz`. Coverage требует Go и Node 22: общий core ≥80%,
отдельно auth/session ≥85% и security ≥90%. Восемь fuzz targets проверяют критические
парсеры; [приёмка 12.2](docs/stage-12-2-acceptance.md) описывает результаты и ограничения.
Те же проверки, включая race detector, выполняет [GitHub Actions workflow](.github/workflows/ci.yml).

`make performance-smoke` проверяет benchmark/SQL fault harness; `make performance`
создаёт полный benchmem/HTTP/SQL report по [зафиксированному протоколу](docs/guide/performance.md).

## Минимальный пример

```go
container := di.New()
_ = container.Provide(func() *Mailer { return &Mailer{} }, di.Singleton())

application := app.New(container, event.NewBus())
application.AddModule(myModule)
if err := application.Run(ctx); err != nil {
    log.Fatal(err)
}
```

Подробный план находится в [PLAN.md](PLAN.md), принятые решения — в [ARCHITECTURE.md](ARCHITECTURE.md).

## HTTP-маршрутизация

```go
router := web.NewRouter()
if err := router.Use(web.Recover); err != nil {
    log.Fatal(err)
}
if err := router.GET("/users/{id}", func(ctx *web.Context) error {
    return ctx.JSON(http.StatusOK, map[string]string{"id": ctx.Param("id")})
}, web.WithName("users.view")); err != nil {
    log.Fatal(err)
}

if err := http.ListenAndServe(":8080", router); err != nil {
    log.Fatal(err)
}
```

Handlers возвращают ошибки, а единый `ErrorHandler` преобразует их в безопасные HTTP-ответы. Это сохраняет явную обработку ошибок, принятую в Go, и выполняет роль централизованного error handler из Yii2.

Для управляемого запуска и graceful shutdown сервер регистрируется как модуль приложения:

```go
server, err := web.NewServer(web.ServerConfig{
    Address:         ":8080",
    ShutdownTimeout: 10 * time.Second,
}, router)
if err != nil {
    log.Fatal(err)
}
if err := application.AddModule(server); err != nil {
    log.Fatal(err)
}

ctx, stop := app.SignalContext(context.Background())
defer stop()
if err := application.Run(ctx); err != nil {
    log.Fatal(err)
}
```

Ошибки, возникшие в `http.Server.Serve` после запуска, передаются в `Application.Run` и инициируют остановку остальных модулей.

`app.SignalContext` обрабатывает interrupt/SIGTERM. Readiness снимается перед quiesce
и reverse drain; rollback, HTTP/workers и exporter finalizers делят общий deadline
(default 15s, `Application.SetShutdownTimeout`). Health probes ограничены временем
и общей concurrency. Порядок подключения, миграция и process-level доказательства —
в [lifecycle и health contract](docs/guide/lifecycle.md).

`Server` и `Router` по умолчанию ограничивают body (8 MiB), формы и uploads до прикладных
middleware; временные multipart-файлы удаляются после handler. `BindJSON` требует JSON
Content-Type (иначе 415), превышение body/form/upload limits возвращает 413.
Server имеет конечные read/header/write/idle timeouts и header budget; для долгих ответов
предусмотрен явный `Streaming` mode. Настройка `RequestLimits`, точные значения и изменения
совместимости описаны в [HTTP resource policy](docs/guide/web.md#ограничения-ресурсов-http).

Шаг 7.2 добавляет `ProxyConfig`, `Router.SetProxyPolicy`/`NewProxyHandler`, проверенные
`Context.ClientIP()`/`Scheme()`/`Host()` и `NewCORSHandler` с preflight/Vary/credentials.
Forwarding headers по умолчанию игнорируются. `Context.Redirect` теперь разрешает root-relative
пути; абсолютные HTTP(S) destinations требуют origin allowlist. Конфигурация и таблица
проверенных proxy-сценариев — в [proxy trust contract](docs/guide/proxy.md).

HTTP compatibility включает HEAD fallback, OPTIONS/405/Allow, segment-wise path escaping,
Range/conditional file responses и streaming через session/debug/logging wrappers.
Контракт, изменения поведения и сетевые тесты — в [HTTP compatibility](docs/guide/http-compatibility.md).

Маршруты можно объединять общим prefix, middleware и доменом:

```go
err := router.Group("/api", func(api *web.Group) error {
    if err := api.Use(authMiddleware); err != nil {
        return err
    }
    if err := api.Host("{tenant}.example.com"); err != nil {
        return err
    }
    return api.GET("/users/{id}", showUser,
        web.WithName("api.users.view"),
        web.Where("tenant", `[a-z][a-z0-9-]*`),
        web.Where("id", `[0-9]+`),
    )
})
if err != nil {
    log.Fatal(err)
}

userURL, err := router.URL("api.users.view", map[string]string{
    "tenant": "acme",
    "id":     "42",
})
// userURL: //acme.example.com/api/users/42
```

Вложенные группы наследуют prefix, host и middleware родителя. Регулярные ограничения применяются как при разборе запроса, так и при обратной генерации URL.

## Контроллеры и actions

Контроллер обычно встраивает `BaseController`, а action объявляется обычным методом с HTTP-контекстом и зависимостями в аргументах:

```go
type UserController struct {
    web.BaseController
}

func (controller *UserController) Show(
    ctx *web.Context,
    users UserRepository,
) error {
    user, err := users.Find(ctx.Request.Context(), ctx.Param("id"))
    if err != nil {
        return err
    }
    return ctx.JSON(http.StatusOK, user)
}

container := di.New()
_ = container.Provide(func() *UserController { return &UserController{} })

err := router.GETAction(
    "/users/{id}",
    container,
    (*UserController).Show,
    web.WithName("users.show"),
)
```

Для собственной логики до и после action контроллер переопределяет `BeforeAction` и `AfterAction`. Ошибка `BeforeAction` отменяет вызов; `AfterAction` получает ошибку action и может вернуть дополнительную ошибку. Контроллер и зависимости action разрешаются через DI для каждого запроса. Те же методы `GETAction`, `POSTAction` и `HandleAction` доступны у групп маршрутов.

## HTTP responses

`Context` предоставляет единый API для основных форматов ответа:

```go
return ctx.HTML(http.StatusOK, "<h1>Chitu</h1>")
return ctx.JSON(http.StatusOK, value)
return ctx.XML(http.StatusOK, value)
return ctx.Stream(http.StatusOK, "text/csv; charset=utf-8", reader)
return ctx.File(http.StatusOK, "storage/report.pdf")
return ctx.Download(http.StatusOK, "storage/report.pdf", "report.pdf")
```

JSON и XML полностью сериализуются до отправки HTTP-статуса, поэтому ошибка кодирования ещё может быть преобразована централизованным `ErrorHandler` в корректный ответ `500`. Для файлов автоматически выставляются `Content-Type`, `Content-Length` и `Last-Modified`; `Download` дополнительно формирует безопасный `Content-Disposition: attachment`. Ошибки чтения и записи возвращаются обработчику с сохранением исходной причины для `errors.Is`/`errors.As`. После начала ответа повторная запись через `Context` отклоняется, а стандартный error handler не дописывает второе тело.

## Views, layouts и widgets

`Renderer` загружает `.html`-шаблоны из любого `fs.FS`. Логическое имя совпадает с путём без расширения:

```text
templates/
  layouts/main.html
  views/home.html
  widgets/alert.html
```

Layout получает готовое содержимое view в `.Content`, а исходные данные — в `.Data`:

```gotemplate
<!doctype html>
<html>
  <head><title>{{.Data.Title}}</title></head>
  <body>{{.Content}}</body>
</html>
```

View может вызывать переиспользуемые widgets и безопасно передавать состояние Vue:

```gotemplate
<h1>{{.Title}}</h1>
{{widget "widgets/alert" .Message}}
{{vueState "app-state" .State}}
```

Инициализация и использование в action:

```go
renderer, err := web.NewRenderer(
    os.DirFS("templates"),
    web.WithDefaultLayout("layouts/main"),
)
if err != nil {
    log.Fatal(err)
}
if err := container.Instance(renderer); err != nil {
    log.Fatal(err)
}
```

Action получает renderer через DI:

```go
func (controller *PageController) Home(
    ctx *web.Context,
    renderer *web.Renderer,
) error {
    data := HomeData{Title: "Home", State: initialState}
    return ctx.Render(http.StatusOK, renderer, "views/home", data)
}
```

Для отдельного view layout можно заменить через `web.WithLayout("layouts/admin")` или отключить через `web.WithoutLayout()`. Дополнительные функции регистрируются опцией `web.WithTemplateFuncs`. После создания `Renderer` можно безопасно использовать конкурентно.

Vue читает состояние без выполнения inline JavaScript:

```js
const state = JSON.parse(document.getElementById("app-state").textContent)
```

JSON экранирует `<`, `>` и `&`, поэтому даже строка `</script>` остаётся внутри data-тега и не превращается в исполняемый HTML.

## Конфигурация

Конфигурация загружается в типизированную структуру. Более поздние источники имеют приоритет: JSON → `.env` → окружение процесса.

```go
type Config struct {
    Debug bool `json:"debug" env:"APP_DEBUG" envDefault:"false"`
    DB struct {
        Host string `json:"host" env:"DB_HOST" envRequired:"true"`
        Port int    `json:"port" env:"DB_PORT" envDefault:"5432"`
    } `json:"db"`
}

var cfg Config
loader := config.New(
    config.WithJSONFile("config/app.json"),
    config.WithEnvFile(".env"),
)
if err := loader.Load(&cfg); err != nil {
    log.Fatal(err)
}
```

## Подключение к базе данных

Пакет `db` использует стандартный `database/sql`. Конкретный драйвер подключается приложением через blank import, а SQL-диалект выбирается независимо от него:

```go
import _ "github.com/jackc/pgx/v5/stdlib"

databaseConfig := db.Config{
    Driver:          "pgx",
    DSN:             "postgres://app:secret@localhost/app",
    Dialect:         db.PostgreSQL,
    MaxOpenConns:    20,
    MaxIdleConns:    5,
    ConnMaxLifetime: 30 * time.Minute,
    ConnMaxIdleTime: 5 * time.Minute,
    PingTimeout:     5 * time.Second,
}

connection, err := db.Open(
    context.Background(),
    databaseConfig,
    db.NewDefaultDialectRegistry(),
)
if err != nil {
    log.Fatal(err)
}
defer connection.Close()
```

`Open` создаёт и настраивает пул, а затем проверяет подключение через `PingContext`. При неудачной проверке пул закрывается. `db.Config` содержит JSON/env-теги и может напрямую загружаться через `config.Loader`; поддерживаются `DB_DRIVER`, `DB_DSN`, `DB_DIALECT`, `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`, `DB_CONN_MAX_LIFETIME`, `DB_CONN_MAX_IDLE_TIME` и `DB_PING_TIMEOUT`.

Встроенный реестр содержит диалекты `db.PostgreSQL`, `db.MySQL` (также MariaDB), `db.Oracle` и `db.SQLServer`. Он отвечает за quoting идентификаторов и placeholders, но не навязывает реализацию драйвера. Для другой СУБД достаточно реализовать интерфейс `db.Dialect` и вызвать `registry.Register(customDialect)`.

Connection делегирует стандартные операции и сохраняет доступ к пулу:

```go
result, err := connection.ExecContext(ctx, query, args...)
rows, err := connection.QueryContext(ctx, query, args...)
row := connection.QueryRowContext(ctx, query, args...)
stats := connection.Stats()
rawDB := connection.DB()
```

### Транзакции

Корневая транзакция использует `database/sql`, а вложенная — savepoint выбранного диалекта:

```go
transaction, err := connection.BeginTx(ctx, &sql.TxOptions{
    Isolation: sql.LevelSerializable,
})
if err != nil {
    return err
}

nested, err := transaction.Begin(ctx)
if err != nil {
    _ = transaction.Rollback()
    return err
}
if _, err := nested.ExecContext(ctx, query, args...); err != nil {
    _ = nested.Rollback()
    _ = transaction.Rollback()
    return err
}
if err := nested.Commit(); err != nil {
    _ = transaction.Rollback()
    return err
}
return transaction.Commit()
```

Для обычного кода удобнее callback API, который автоматически выполняет commit или rollback:

```go
err := connection.Transaction(ctx, nil, func(transaction *db.Transaction) error {
    if _, err := transaction.ExecContext(ctx, firstQuery, firstArgs...); err != nil {
        return err
    }
    return transaction.Transaction(ctx, func(nested *db.Transaction) error {
        _, err := nested.ExecContext(ctx, secondQuery, secondArgs...)
        return err
    })
})
```

При ошибке callback откатывается соответствующий уровень; при panic rollback выполняется перед повторным panic. Если callback оставил активную вложенную транзакцию, фреймворк откатывает всю её цепочку и возвращает lifecycle-ошибку. Пока дочерняя транзакция активна, операции родителя возвращают `db.ErrNestedTransactionActive`. Повторное завершение возвращает `db.ErrTransactionDone`.

Встроенные диалекты поддерживают разный синтаксис savepoints: PostgreSQL/MySQL — `SAVEPOINT` и `RELEASE SAVEPOINT`, Oracle — `SAVEPOINT`/`ROLLBACK TO`, SQL Server — `SAVE TRANSACTION`/`ROLLBACK TRANSACTION`. Сторонний диалект включает вложенность реализацией `db.SavepointDialect`; иначе `Begin` возвращает `db.ErrSavepointsUnsupported`.

### Query Builder

`db.QueryBuilder` неизменяем: каждый fluent-вызов возвращает независимый запрос, поэтому общую основу можно безопасно переиспользовать. Идентификаторы экранируются выбранным диалектом, а значения передаются драйверу отдельно от SQL:

```go
base := db.NewQueryBuilder(connection.Dialect()).
    Select("u.id", "u.email").
    From("users", "u").
    Where(db.Eq("u.active", true))

query := base.
    AndWhere(db.In("u.role", "admin", "editor")).
    OrderBy("u.email", db.SortAscending).
    Limit(50)

sqlText, args, err := query.Build()
if err != nil {
    return err
}
rows, err := query.QueryContext(ctx, connection)
```

Поддерживаются `SELECT`, `FROM`, типизированные `JOIN`, `WHERE`, `GROUP BY`, `HAVING`, `ORDER BY`, `LIMIT`/`OFFSET`, а также `INSERT`, `UPDATE` и `DELETE`. Условия `Eq`, `Ne`, `Gt`, `Gte`, `Lt`, `Lte`, `Like`, `In`, `NotIn`, `IsNull`, `IsNotNull`, `And`, `Or` и `Not` всегда создают placeholders. Нумерация и пагинация учитывают PostgreSQL, MySQL/MariaDB, Oracle и SQL Server; для `OFFSET` в SQL Server требуется `ORDER BY`, а один `LIMIT` преобразуется в `TOP`.

`SelectRaw`, `SelectRawAs` и `RawCondition` предназначены только для SQL-фрагментов, заданных разработчиком; их значения всё равно передаются отдельными аргументами через маркер `?` (`??` означает литеральный вопросительный знак). `UPDATE` и `DELETE` без `WHERE` отклоняются, пока намерение не подтверждено явным вызовом `AllRows()`.

### Миграции

Пакет `db/migration` загружает парные SQL-файлы из каталога миграций. Имя содержит UTC-версию и описание:

```text
migrations/
  20261101090000_create_users.up.sql
  20261101090000_create_users.down.sql
```

`Runner.Up` применяет ожидающие миграции по порядку, `Runner.Down` откатывает последние в обратном порядке, а `Runner.Status` возвращает состояния `pending`, `applied`, `modified` и `missing`. История хранится в таблице `chitu_migration`; SHA-256 блокирует применение и откат изменённого файла. SQL и запись истории выполняются в одной транзакции, если выбранная СУБД поддерживает транзакционный DDL.

CLI встраивается в консольное приложение, где регистрируется нужный `database/sql`-драйвер:

```go
import (
    "context"
    "log"
    "os"

    _ "github.com/jackc/pgx/v5/stdlib"
    "github.com/chitushka/framework/db/migration"
)

func main() {
    cli := migration.NewCLI()
    if err := cli.Run(context.Background(), os.Args[1:]); err != nil {
        log.Fatal(err)
    }
}
```

Доступны команды `migration:create NAME`, `migration:up`, `migration:down` и `migration:status`. Общие флаги: `-dir`, `-driver`, `-dsn`, `-dialect`, `-table`; `up` и `down` принимают `-limit`. Значения подключения также читаются из `DB_DRIVER`, `DB_DSN`, `DB_DIALECT`, а настройки миграций — из `MIGRATION_DIR` и `MIGRATION_TABLE`.

### Active Record

Пакет `db/ar` связывает типизированный `Repository[T]` с обычной Go-структурой. Схема задаётся тегами `db`; поддерживаются составные первичные ключи, auto-ID и поле optimistic locking:

```go
type User struct {
    ID      int64  `db:"id,primary,auto" json:"id"`
    Email   string `db:"email" json:"email"`
    Version int64  `db:"version,version" json:"version"`
}

func (User) TableName() string { return "users" }

users, err := ar.NewRepository[User](connection, ar.Options{})
user, err := users.FindByPK(ctx, int64(42))

active, err := users.Find().
    Where(db.Like("email", "%@example.com")).
    OrderBy("id", db.SortDescending).
    Limit(20).
    All(ctx)

err = users.Save(ctx, user)
err = users.Delete(ctx, user)
```

`Insert`, `Update`, `Delete` и `Save` используют безопасный Query Builder. При обновлении поле `version` увеличивается в SQL вместе с проверкой прежнего значения; нулевой `RowsAffected` возвращает `ar.ErrStaleModel`. Репозиторий можно переключить на `*db.Transaction` через `WithExecutor`.

Связи задаются через `ar.Relation`. `HasOne`/`HasMany` выполняют ленивую загрузку одной модели, а `LoadHasOne`/`LoadHasMany` загружают связи для среза родителей одним `IN`-запросом и возвращают результаты в том же порядке.

Генератор строит gofmt-форматированные модели, теги и типизированные методы связей из `ar.TableSchema`. `ar.DatabaseInspector` получает столбцы и их типы через переносимый `database/sql.ColumnTypes`; первичные ключи, auto/version и связи передаются в `ar.InspectOptions`, поскольку эти метаданные не имеют единого контракта у SQL-драйверов. `Generator.GenerateFile` создаёт файл эксклюзивно и не перезаписывает существующую модель.

## Валидация и безопасное присваивание

Пакет `validation` отделяет пользовательские нарушения правил от инфраструктурных ошибок и поддерживает разные контракты для `create`, `update` и других сценариев:

```go
type UserInput struct {
    ID      int64  `json:"id"`
    Email   string `json:"email"`
    Name    string `json:"name"`
    IsAdmin bool   `json:"is_admin"`
}

func (*UserInput) Validation() validation.Set {
    return validation.Set{
        Scenarios: map[string]validation.Scenario{
            "create": {Fields: []string{"email", "name"}},
            "update": {Fields: []string{"name"}},
        },
        Rules: []validation.Rule{
            validation.Field("email", validation.Required{}).In("create"),
            validation.Field("email", validation.Email{}),
            validation.Field("name", validation.String{Min: 2, Max: 100}),
        },
    }
}

input := &UserInput{}
assigned, err := validation.AssignModel(input, payload, "create")
validationErrors, err := validation.ValidateModel(ctx, input, "create")
```

`Scenario.Fields` является явным allowlist: поля `id` и `is_admin` из примера никогда не присваиваются из `payload`. Сначала конвертируются все допустимые значения и только затем модель изменяется, поэтому ошибка типа или переполнение не оставляют частично заполненную структуру. Результат сообщает списки назначенных и проигнорированных ключей.

Встроены правила `Required`, `String`, `Integer`, `Email`, `Match`, `Compare`, `Date`, `File`, `Safe`, а также DB-проверки `Unique` и `Exist` с безопасными параметрами Query Builder. Пользовательские правила реализуются через малый интерфейс `Validator` или `ValidatorFunc`; все нарушения накапливаются по полям в `validation.Errors`.

## Кэширование

Пакет `cache` предоставляет единый context-aware API с TTL, удалением, полной очисткой логического пространства, тегами и зависимостями. Данные хранятся как `[]byte`, поэтому тот же API подходит для результатов запросов, JSON и серверных HTML-фрагментов:

```go
backend := cache.NewMemoryBackend()
storage, err := cache.New(backend, "main")
if err != nil {
    log.Fatal(err)
}

err = storage.Set(ctx, "user:42", payload, cache.Options{
    TTL:  10 * time.Minute,
    Tags: []string{"users", "user:42"},
})

payload, found, err := storage.Get(ctx, "user:42")
err = storage.InvalidateTags(ctx, "users")
```

Встроенные backend-драйверы:

- `NewMemoryBackend` — потокобезопасное in-process хранилище с ленивым удалением истёкших записей;
- `NewRedisBackend` — RESP2-клиент с AUTH, SELECT, префиксом и сетевыми тайм-аутами;
- `NewMemcachedBackend` — клиент текстового протокола с корректной относительной и абсолютной экспирацией;
- `NewDatabaseBackend` — SQL-хранилище для PostgreSQL, MySQL/MariaDB, Oracle и SQL Server. Метод `SchemaSQL` возвращает DDL, который следует применить обычной миграцией приложения.

`DependencyFunc` позволяет привязать запись к произвольному состоянию, а `DBDependency` вычисляет SHA-256 полного упорядоченного результата SQL-запроса. Для стабильного отпечатка запрос должен содержать `ORDER BY`. Вычислители зависимостей являются Go-значениями и живут в singleton-экземпляре `Store`: если запись с callback-зависимостью прочитана другим процессом, она безопасно считается устаревшей. Теги и `Clear` используют поколения в backend и поэтому работают между процессами без сканирования ключей; старые записи с ограниченным TTL удаляются самим backend, поэтому для изменяемых данных рекомендуется всегда задавать TTL.

## Компоненты и behaviors

Именованные компоненты приложения создаются лениво и по умолчанию работают как singleton:

```go
err := application.Components.Register("mailer", func(
    ctx context.Context,
    resolver component.Resolver,
) (any, error) {
    return NewMailer(cfg.SMTP), nil
})
if err != nil {
    log.Fatal(err)
}

mailer, err := component.GetAs[*Mailer](ctx, application.Components, "mailer")
if err != nil {
    log.Fatal(err)
}
```

Аналог Yii2 behaviors реализован явными декораторами интерфейсов:

```go
service, err := behavior.Apply[UserService](
    baseService,
    WithAudit(auditLog),
    WithMetrics(metrics),
)
if err != nil {
    log.Fatal(err)
}
```

## Сессии, аутентификация и CSRF

Серверная сессия подключается глобальным middleware и требует `session.AtomicBackend`: конфликтующий commit возвращает 409, смена ID атомарна, старый снимок не восстанавливает отозванную сессию. Memory backend подходит для одного процесса; внешний общий AtomicBackend предоставляет приложение. Простые cache Get/Set/Delete adapters отклоняются. Для небольшого некритичного stateless-состояния доступен зашифрованный CookieStore с idle/absolute TTL и key rotation, без индивидуального revocation. Полный [контракт sessions/CSRF и миграция](docs/guide/sessions.md).

```go
sessions, err := session.New(session.NewMemoryBackend(), session.Config{
    Name:   "sid",
    TTL:    24 * time.Hour,
    Secure: true,
})
if err != nil {
    log.Fatal(err)
}

_ = router.Use(
    sessions.Middleware,
    auth.Middleware(identityLoader),
    security.CSRF(security.CSRFConfig{}),
    security.Headers(security.HeadersConfig{
        ContentSecurityPolicy: "default-src 'self'",
        HSTS: "max-age=31536000; includeSubDomains",
    }),
)
```

Текущие объекты доступны только в контексте запроса:

```go
currentSession := session.Must(ctx.Request.Context())
currentUser := auth.Must(ctx.Request.Context())

if err := currentUser.Login(identity); err != nil {
    return err
}
return ctx.JSON(http.StatusOK, map[string]string{
    "id": currentUser.ID(),
})
```

CSRF-токен возвращается в заголовке `X-CSRF-Token`. Небезопасные методы должны прислать его в том же заголовке (Vue/fetch) или в поле формы `_csrf`. `Login` и `Logout` меняют идентификатор сессии; cookie имеет `HttpOnly` и `SameSite=Lax` по умолчанию.

## RBAC

`rbac.Manager` управляет ролями, разрешениями, их иерархией и назначениями пользователей.
Для одного процесса доступен `MemoryStore`; `DatabaseStore` использует три нормализованные
SQL-таблицы и генерирует SQL для встроенных диалектов PostgreSQL, MySQL, Oracle и SQL Server.
Этот store проверен fake driver; реальный multi-process RBAC и MariaDB пока вне baseline.

```go
manager, err := rbac.New(rbac.NewMemoryStore())
if err != nil {
    log.Fatal(err)
}

ctx := context.Background()
_ = manager.AddRole(ctx, "editor", "Content editor")
_ = manager.AddPermission(ctx, "post.update", "Update a post")
_ = manager.AddChild(ctx, "editor", "post.update")
_ = manager.Assign(ctx, "user-42", "editor")

allowed, err := manager.CheckAccess(ctx, "user-42", "post.update", nil)
```

Контекстное правило связывается с элементом по имени и получает параметры проверки:

```go
_ = manager.RegisterRule("ownsPost", rbac.RuleFunc(func(
    ctx context.Context,
    userID string,
    params map[string]any,
) (bool, error) {
    return params["ownerID"] == userID, nil
}))

_ = manager.AddItem(ctx, rbac.Item{
    Name: "post.update.own",
    Kind: rbac.Permission,
    Rule: "ownsPost",
})
```

Для контроллеров проверка подключается route middleware. Гость получает `401`, а аутентифицированный пользователь без разрешения — `403`:

```go
_ = router.GET("/posts/{id}/edit", editPost,
    web.WithMiddleware(rbac.Require(manager, "post.update", func(ctx *web.Context) map[string]any {
        return map[string]any{"ownerID": loadOwnerID(ctx.Param("id"))}
    })),
)
```

В шаблон можно зарегистрировать `manager.TemplateFunc(ctx, params)` как функцию `can`. Для SQL-хранилища `DatabaseStore.SchemaSQL()` возвращает DDL, который следует применить обычной миграцией проекта.

## Очереди и воркеры

`queue.Queue` сериализует payload в JSON и отправляет его выбранному broker. Встроенный `MemoryBroker` подходит для разработки; `DatabaseBroker` обеспечивает межпроцессную резервируемую очередь. `RedisBroker` и `RabbitMQBroker` подключаются через малые интерфейсы; готовые SDK implementations поставляются отдельным модулем `github.com/chitushka/framework/adapters`.

```go
broker := queue.NewMemoryBroker()
jobs, err := queue.New(broker)
if err != nil {
    log.Fatal(err)
}

type ReportJob struct {
    ReportID string `json:"reportId"`
}

jobID, err := jobs.Push(ctx, "report.generate", ReportJob{
    ReportID: "monthly-2026-11",
}, queue.Options{
    Delay:       5 * time.Second,
    MaxAttempts: 5,
})
```

Worker регистрирует типизированные обработчики, выполняет несколько задач параллельно и повторяет временные ошибки:

```go
deadLetters, err := queue.NewFileDeadLetters("./var/deadletters")
if err != nil {
    log.Fatal(err)
}
defer deadLetters.Close()

worker, err := queue.NewWorker(broker, queue.WorkerConfig{
    DeadLetters: deadLetters,
    Concurrency: 4,
    OnFailure: func(failure queue.Failure) {
        slog.Error("job failed",
            "id", failure.Message.ID,
            "final", failure.Final,
            "error", failure.Err,
        )
    },
})
if err != nil {
    log.Fatal(err)
}

err = worker.RegisterHandler("report.generate", queue.JSONHandler(
    func(ctx context.Context, job ReportJob) error {
        return reports.Generate(ctx, job.ReportID)
    },
))
if err != nil {
    log.Fatal(err)
}

_ = application.AddModule(worker)
```

`DatabaseBroker.SchemaSQL()` возвращает DDL таблицы очереди для текущего диалекта.
Baseline PostgreSQL/MySQL, Redis и RabbitMQ проверены на process/transport recovery.
Lease fencing защищает от stale Ack; RabbitMQ ждёт publisher confirms. Семантика —
at-least-once: обработчики должны быть идемпотентными. Финальная ошибка сохраняется
в durable dead-letter до Ack, доступны List/Get/Replay. Store должен жить на persistent
volume; disk/host crash не подтверждён этим стендом. [Полный контракт](docs/guide/queues.md)
описывает budgets, drain, SDK adapters, quarantine, outbox и ограничения AMQP delay/retry.

## Интернационализация

Каталоги переводов размещаются в любом `fs.FS` по схеме `<locale>/<category>.json`:

```text
locales/
  en/app.json
  ru/app.json
```

Простой текст задаётся строкой, а plural message — объектом форм:

```json
{
  "welcome": "Привет, {{.name}}!",
  "items": {
    "one": "{{.count}} предмет",
    "few": "{{.count}} предмета",
    "many": "{{.count}} предметов",
    "other": "{{.count}} предмета"
  }
}
```

Загрузка и подключение к HTTP-приложению:

```go
catalog, err := i18n.LoadFS(os.DirFS("locales"))
if err != nil {
    log.Fatal(err)
}

translator, err := i18n.New(catalog, i18n.Config{
    DefaultLocale:    "en",
    SupportedLocales: []string{"en", "ru", "fr"},
    Fallbacks: map[string][]string{
        "fr-CA": {"fr"},
    },
})
if err != nil {
    log.Fatal(err)
}

_ = router.Use(i18n.Middleware(translator, i18n.MiddlewareConfig{}))
```

Middleware выбирает язык в порядке `?lang=`, cookie `lang`, `Accept-Language`, default и помещает канонический locale в контекст запроса:

```go
message, err := translator.T(
    ctx.Request.Context(),
    "app",
    "welcome",
    map[string]any{"name": user.Name},
)

items, err := translator.N(
    ctx.Request.Context(),
    "app",
    "items",
    int64(len(records)),
    nil,
)
```

Для `html/template` доступны locale-explicit функции `t` и `tn` через `translator.TemplateFuncs()`. Их результат остаётся строкой и автоматически экранируется шаблонизатором. По умолчанию отсутствующий перевод возвращает message key; строгий режим включается через `ErrorOnMissing`.

## Console и генераторы

`console.Application` объединяет команды приложения и предоставляет им тот же lifecycle, DI и component registry, что использует web-приложение:

```go
func main() {
    core := app.New(nil, nil)
    cli := console.New(core)

    if err := console.RegisterTools(cli, console.ToolOptions{}); err != nil {
        log.Fatal(err)
    }
    if err := cli.Run(context.Background(), os.Args[1:]); err != nil {
        log.Fatal(err)
    }
}
```

Конкретный `database/sql`-драйвер регистрируется приложением через blank import. Параметры `-driver`, `-dsn`, `-dialect` можно заменить переменными `DB_DRIVER`, `DB_DSN`, `DB_DIALECT`.

Встроенные команды:

- `migration:create`, `migration:up`, `migration:down`, `migration:status`;
- `generate:migration` — создаёт диалектную `CREATE/DROP TABLE` migration по метаданным таблицы;
- `generate:model` — создаёт Active Record struct с db/json tags;
- `generate:crud` — создаёт controller, allowlist search/input models и Vue 3/AdminKit scaffold.

Примеры:

```bash
app generate:migration -driver pgx -dialect postgresql -pk id -auto id -dir migrations users

app generate:model -driver pgx -dialect postgresql -pk id -auto id \
  -package model -output internal/model/user.go users

app generate:crud -driver pgx -dialect postgresql -pk id -auto id \
  -model-package example.com/project/internal/model \
  -package controller -output internal/controller \
  -vue-output web/src/views/UserView.vue -route /users users
```

CRUD controller предоставляет list/show/create/update/delete JSON actions и функцию регистрации маршрутов. Генерируемый input не позволяет массово присвоить primary key, auto-ID или optimistic-lock version; search model принимает только колонки исходной схемы. Числовые и boolean Vue-поля отправляются с корректными JSON-типами. Генераторы никогда не перезаписывают существующие файлы.

## Vue 3, Vite, AdminKit и AssetBundle

Каталог `frontend` содержит готовый Vue 3 scaffold на AdminKit 3.5.0: dashboard, профиль, вход, регистрация, восстановление пароля и страницы 404/500. Для разработки и production-сборки:

```bash
cd frontend
npm ci
npm run dev
npm run build
```

Vite пишет production-ресурсы в `public`, включая `.vite/manifest.json`. Их хэшированные имена подключаются без ручного копирования в Go-шаблоны:

```go
manifest, err := asset.LoadViteManifest(os.DirFS("public"), ".vite/manifest.json")
if err != nil {
    log.Fatal(err)
}

application, err := manifest.Bundle("src/main.js", "/")
if err != nil {
    log.Fatal(err)
}

assets := asset.NewRegistry()
if err := assets.Register("application", application); err != nil {
    log.Fatal(err)
}

renderer, err := web.NewRenderer(
    os.DirFS("templates"),
    web.WithDefaultLayout("layouts/main"),
    web.WithTemplateFuncs(assets.TemplateFuncs("application")),
)
```

Layout размещает стили и module preload в `head`, а scripts — перед закрытием `body`:

```gotemplate
<!doctype html>
<html>
  <head>{{assetHead}}</head>
  <body>{{.Content}}{{assetBody}}</body>
</html>
```

В режиме разработки вместо manifest регистрируется `asset.ViteDevBundle("http://localhost:5173", "src/main.js")`: он добавляет HMR client и entry как ES modules. Общие библиотеки можно оформить отдельными bundles и перечислить в `Dependencies`; реестр обеспечит порядок, дедупликацию и обнаружит цикл.

## Логи, метрики и tracing

Logging построен поверх стандартного `log/slog`. Console, append-only file и произвольные targets объединяются независимо от формата и уровня:

```go
console, _ := logging.NewWriterHandler(os.Stdout, logging.FormatJSON, &slog.HandlerOptions{
    Level: slog.LevelInfo,
})
file, closeLog, err := logging.OpenFileHandler("runtime/application.log", logging.FormatJSON, nil)
if err != nil {
    log.Fatal(err)
}
defer closeLog.Close()

targets, _ := logging.NewMultiHandler(console, file, elasticsearchHandler)
logger, _ := logging.New(targets)
logger.Info("application started", "version", version)
```

`Debug`, `Info`, `Warn` и `Error` наследуются от `slog.Logger`; `Fatal` пишет запись на уровне `logging.LevelFatal` и завершает процесс. DB, Syslog, Elasticsearch или Telegram подключаются обычной реализацией `slog.Handler` и могут иметь собственный threshold.

HTTP-наблюдаемость подключается снаружи router, поэтому видит в том числе 404/405 и ответы централизованного error handler:

```go
metrics, _ := observability.NewMetrics()
tracer, _ := observability.NewTracer(applicationSpanExporter)
// После HTTP drain вызовите tracer.Shutdown с независимым context и проверьте ошибку.

handler := observability.HTTP(observability.HTTPConfig{
    Logger:  logger.Logger,
    Metrics: metrics,
    Tracer:  tracer,
})(router)

http.Handle("/", handler)
http.Handle("/metrics", metrics)
```

Prometheus endpoint экспортирует `chitu_http_requests_total`, `chitu_http_errors_total`, active requests и histogram времени ответа с ограниченными method/status labels. Tracer поддерживает W3C `traceparent`; текущие trace/span ID доступны через `observability.SpanFromContext`. Legacy `SpanExporter` теперь вызывается асинхронно через очередь 256 spans; для чтения результата нужен Flush, после drain — Shutdown. Stats и ErrExport показывают потери/ошибки.

Готовый `github.com/chitushka/framework/adapters/otel` связывает HTTP → SQL → queue → worker через OpenTelemetry SDK/OTLP HTTP. Он поставляет sampling, bounded batching и flush/shutdown, explicit TLS/auth и redaction без обязательного SDK в ядре. Конфигурация, пример подключения, SQL boundaries и breaking migration legacy tracer — в [observability guide](docs/guide/observability.md). Полный `scripts/integration.sh` / `scripts/integration.ps1` проверяет сквозную трассу на настоящем collector и отдельном worker process.

Для CPU/heap/goroutine profiling доступен `observability.PprofHandler()`; endpoint размещается на закрытом listener или защищается авторизацией.

## Debug toolbar

Toolbar включается только в development-конфигурации:

```go
toolbar, err := debug.New(debug.Config{
    Enabled:        environment == "development",
    Environment:    []string{"APP_ENV", "APP_VERSION"}, // явный allowlist
    CaptureSQLArgs: false,
    Session: func(ctx context.Context) map[string]any {
        user := auth.Must(ctx)
        return map[string]any{"user_id": user.ID()}
    },
})
if err != nil {
    log.Fatal(err)
}
_ = router.Use(toolbar.Middleware)
```

Для запросов через `db.Connection` и `db.Transaction` SQL, duration и ошибки собираются автоматически из request context. Аргументы скрыты по умолчанию. Если приложение безопасно получило план выполнения, его можно прикрепить вызовом `debug.RecordSQLPlan(ctx, query, plan)`. Панель добавляется только в успешный несжатый HTML и показывает также route/params, память, session snapshot, разрешённые environment values и trace ID.

Не включайте toolbar и pprof публично в production. Environment и session не читаются автоматически: приложение явно определяет безопасный набор значений.

Пользовательская команда задаётся без наследования:

```go
_ = cli.Register(console.CommandFunc{
    CommandName:        "report:daily",
    CommandDescription: "build the daily report",
    Execute: func(ctx context.Context, command *console.Context, args []string) error {
        reports, err := component.GetAs[*ReportService](ctx, command.Application.Components, "reports")
        if err != nil {
            return err
        }
        return reports.BuildDaily(ctx)
    },
})
```
