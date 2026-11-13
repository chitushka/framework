# Начало работы

## Требования и выбор starter

Нужен Go версии не ниже `go` из корневого `go.mod` (сейчас 1.26.11).
Первая загрузка Go dependencies требует сети. Для минимального HTTP/worker/generator
не нужны Node, SQL или Docker. Browser starters требуют Node.js/npm из
`frontend/package.json`; real-service/browser acceptance — Docker Engine с Linux
containers и Compose v2. Поддержка платформ — в [support](support.md).

Работа с исходниками:

```bash
git clone https://github.com/chitushka/framework.git
cd framework
go mod download
go run ./tools/checkdocs -run
```

Если checkout уже открыт, начинайте с `go mod download`. Docs command проверяет
ссылки/snippets и запускает примеры в отдельном временном Go-модуле, включая сборку
сгенерированного CRUD. На Windows команды Go те же; нужен только установленный Go.

| Нужен результат | Начать с |
| --- | --- |
| Минимальный HTTP/JSON и health | Пример ниже |
| Browser login/dashboard с CSRF | [Basic](../../examples/basic/README.md), `node scripts/basic.mjs start` |
| SQL пользователи/роли/справочник | [Advanced Admin](../../examples/advanced-admin/README.md) |
| JWT API с CRUD/OpenAPI | [REST](../../examples/rest-api/README.md) |
| Model/CRUD/Vue/DDL scaffold без SQL | [Generator quickstart](generator-quickstart.md) |

## Первый HTTP-сервис

Из корня checkout:

```bash
go run ./examples/guide/http
```

В другом терминале проверьте `http://127.0.0.1:8080/hello` (JSON message),
`/livez` и `/readyz` (200 во время работы). На Linux/macOS можно использовать
`curl http://127.0.0.1:8080/hello`, на Windows —
`Invoke-RestMethod http://127.0.0.1:8080/hello`. Ctrl-C запускает bounded shutdown.
Bind только на loopback; это публичный read-only hello, без login/authorization.

```go file=../../examples/guide/http/main.go
// Command http is a minimal loopback service with bounded shutdown and health.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/web"
)

func service(address string) (*app.Application, *web.Server, error) {
	application := app.New(nil, nil)
	if err := application.SetShutdownTimeout(7 * time.Second); err != nil {
		return nil, nil, err
	}
	health, err := web.NewHealth(application, web.HealthConfig{ProbeTimeout: 200 * time.Millisecond})
	if err != nil {
		return nil, nil, err
	}
	router := web.NewRouter()
	if err := router.GET("/hello", func(ctx *web.Context) error {
		return ctx.JSON(http.StatusOK, map[string]string{"message": "Hello, Chitu"})
	}); err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", health.Liveness)
	mux.HandleFunc("/readyz", health.Readiness)
	mux.Handle("/", router)
	handler, err := web.NewProductionHandler(mux)
	if err != nil {
		return nil, nil, err
	}
	server, err := web.NewServer(web.ServerConfig{Address: address, ShutdownTimeout: 5 * time.Second}, handler)
	if err != nil {
		return nil, nil, err
	}
	if err := application.AddModule(server); err != nil {
		return nil, nil, err
	}
	return application, server, nil
}

func main() {
	application, _, err := service("127.0.0.1:8080")
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
```

`web.Server` применяет конечные resource/time budgets, `app.SignalContext`
обрабатывает Interrupt/SIGTERM, health различает liveness и readiness. Production
wrapper скрывает framework diagnostics/errors, но не добавляет authentication.
Readiness без dependency checks отражает только lifecycle; SQL readiness probe
добавляется явно. [HTTP test](../../examples/guide/http/main_test.go) запускает
настоящий server на свободном loopback port и проверяет JSON и оба health endpoint.

## Отдельный новый Go-модуль

Вместо неприкреплённого `go get ...@latest` используйте текущий checkout как
local dependency: pre-v1 release tag ещё не является принятым v1.0. Из корня
checkout в Linux/macOS shell, с новым отсутствующим каталогом `../chitu-hello`:

```bash
framework_root="$PWD"
mkdir ../chitu-hello
cp examples/guide/http/main.go ../chitu-hello/main.go
cd ../chitu-hello
go mod init example.com/hello
go mod edit -require=github.com/chitushka/framework@v0.0.0
go mod edit "-replace=github.com/chitushka/framework=$framework_root"
go mod tidy
go run .
```

PowerShell: перед переходом задайте `$frameworkRoot = (Get-Location).Path`,
создайте каталог через `New-Item -ItemType Directory ../chitu-hello` и скопируйте
`Copy-Item examples/guide/http/main.go ../chitu-hello/main.go`.
Go-команда replace: `go mod edit "-replace=github.com/chitushka/framework=$frameworkRoot"`.
`v0.0.0` — placeholder для local replace. Позже закрепите выбранный принятый
version tag и удалите local replace по [support policy](support.md).

## Структура и следующий шаг

```text
cmd/server/main.go       composition root
internal/domain/         сущности и бизнес-правила
internal/service/        прикладные сценарии
internal/repository/     хранение
internal/http/           handlers и middleware
migrations/              SQL up/down
frontend/                Vue/Vite, если нужен browser UI
```

Интерфейсы репозиториев размещайте рядом с потребителем; конкретные реализации
собирайте в main через явные зависимости/DI. Затем пройдите
[приложение и модули](application.md), [cookbook](cookbook/README.md) и
[API migration guide](api-migration.md). Проверки и ограничения docs gate —
в [testing recipe](cookbook/testing.md#проверка-документации).
