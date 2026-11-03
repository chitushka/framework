# Начало работы

## Требования

- Go версии из директивы `go` в `go.mod`;
- Docker — опционально, для одинаковых проверок на разных рабочих станциях;
- Node.js и npm — только при изменении Vue frontend.

Подключите модуль:

```bash
go get github.com/chitushka/framework
```

## Первый HTTP-сервис

```go
package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "os/signal"

    "github.com/chitushka/framework/app"
    "github.com/chitushka/framework/web"
)

func main() {
    router := web.NewRouter()
    if err := router.GET("/health", func(ctx *web.Context) error {
        return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
    }); err != nil {
        log.Fatal(err)
    }

    server, err := web.NewServer(web.ServerConfig{Address: ":8080"}, router)
    if err != nil {
        log.Fatal(err)
    }
    application := app.New(nil, nil)
    if err := application.AddModule(server); err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()
    if err := application.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

Handlers возвращают ошибки, а не записывают их вручную. Централизованный error handler формирует безопасный JSON-ответ. `web.Server` участвует в жизненном цикле приложения и при отмене контекста выполняет graceful shutdown.

## Рекомендуемая структура

```text
cmd/server/main.go       точка входа и composition root
internal/config/         типизированная конфигурация
internal/domain/         сущности и бизнес-правила
internal/service/        прикладные сценарии
internal/repository/     реализация хранения
internal/http/           handlers, controllers, middleware
migrations/              SQL-миграции
frontend/                Vue-приложение, если оно требуется
```

Не импортируйте HTTP или SQL в доменную модель. Интерфейсы репозиториев располагайте рядом с потребителем, а конкретные реализации собирайте в `main` через DI.

## Следующий шаг

Запустите один из [готовых примеров](../../examples/README.md), затем перейдите к [приложению и модулям](application.md).
