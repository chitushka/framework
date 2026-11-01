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
