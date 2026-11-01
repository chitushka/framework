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
