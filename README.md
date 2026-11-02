# Chitu Framework

Chitu — модульный веб-фреймворк с module path `github.com/chitushka/framework`. Архитектура и удобство разработки опираются на Yii2, а реализация следует идиоматическому Go без буквального переноса динамических PHP-механизмов. Проект находится на раннем этапе: сейчас доступны DI-контейнер, Event Bus и управляемый жизненный цикл модулей.

## Проверка через Docker

Локальная установка Go не требуется:

```bash
docker build --target test -t chitu-test .
```

Для интерактивной разработки:

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26 go test ./...
```

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

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()
if err := application.Run(ctx); err != nil {
    log.Fatal(err)
}
```

Ошибки, возникшие в `http.Server.Serve` после запуска, передаются в `Application.Run` и инициируют остановку остальных модулей.

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

Серверная сессия подключается глобальным middleware. Встроенный memory backend подходит для разработки и одного процесса; Redis и Database backends пакета `cache` подключаются через `session.NewBackend`. Для небольших stateless-сессий доступен зашифрованный `CookieStore`.

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

`rbac.Manager` управляет ролями, разрешениями, их иерархией и назначениями пользователей. Для одного процесса доступен `MemoryStore`; `DatabaseStore` использует три нормализованные SQL-таблицы и поддерживает встроенные диалекты PostgreSQL, MySQL/MariaDB, Oracle и SQL Server.

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

`queue.Queue` сериализует payload в JSON и отправляет его выбранному broker. Встроенный `MemoryBroker` подходит для разработки; `DatabaseBroker` обеспечивает межпроцессную резервируемую очередь. `RedisBroker` и `RabbitMQBroker` подключаются через малые адаптеры клиентских библиотек, не фиксируя конкретный Redis/AMQP SDK.

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
worker, err := queue.NewWorker(broker, queue.WorkerConfig{
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

`DatabaseBroker.SchemaSQL()` возвращает DDL таблицы очереди для текущего диалекта. Reservation lease автоматически возвращает задачу в обработку после падения процесса. Гарантия доставки — at least once, поэтому обработчики должны быть идемпотентными. Для Redis адаптер `RedisStore` должен атомарно резервировать due-message и восстанавливать истёкшие leases; RabbitMQ-адаптер реализует `RabbitChannel` поверх выбранного AMQP-клиента.

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
