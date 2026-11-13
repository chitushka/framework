# Testing из checkout

## Быстрая обратная связь

Нужны Go из `go.mod` и доступ к Go proxy при первом получении зависимостей.
Из корня репозитория:

```bash
go mod download
go vet ./...
go test ./...
go run ./tools/checkdocs -run
```

Последняя команда не зависит от Node, SQL, Docker, постоянного каталога генерации
или заданных JWT/secrets. Она использует только prefetched Go dependencies;
временные source/module/output directories создаются заново и удаляются после run.
Собственный `go.work` отключён, сеть для fixture modules отключена через GOPROXY=off.
Для Linux с CGO добавьте `go test -race ./...`. На Windows без C compiler используйте
обычный `go test`; отдельный CI job выполняет Linux race.

## Проверка документации

```bash
go run ./tools/checkdocs
go run ./tools/checkdocs -run
```

Первый режим сканирует repository Markdown, исключая dependency/build directories:
`.git`, `.cache`, `.idea`, `.vscode`, `node_modules`, `public`, `vendor`. Проверяются
inline links/images, явные reference links `[text][id]`, локальные paths и ATX
heading anchors, включая Unicode и повторные headings. Case проверяется также на
Windows. Поддерживаются явные HTML `a/span id/name`. Локальные ссылки не могут
выходить из repository через `..` или symlink. Fenced/inline code не считается
текстом со ссылками. Reference shortcuts и произвольный HTML не входят в формат
этого checker: пишите проверяемые Markdown links или явные references.

HTTP(S)/mailto links проверяются синтаксически, без сетевых запросов: доступность
upstream страниц, redirects и их содержание этим gate не подтверждаются.
Fences вида `go file=relative/source.go` сверяются байт-в-байт с исходником после
нормализации CRLF. Для каждого `examples/guide/*/main.go` требуется такой fence.

`-run` дополнительно копирует три guide packages в отдельный внешний module и
выполняет их tests: настоящий loopback HTTP/health, worker completion/cancellation,
генерация model/CRUD/Vue/DDL и компиляция generated project с input-policy test.
Команды из Markdown не исполняются; secrets/production DSN инструменту не нужны.
Общий timeout 3 минуты, generated-project subprocess — 2 минуты; провал команды
или cleanup даёт ненулевой exit code. Negative tests checker проверяют, что битые
paths/anchors/references, stale source fence и пустое дерево отклоняются.

CI job `docs` сохраняет test report и выполняет этот же entry point. Windows matrix
также запускает checker с isolated module. `make docs` и `make docs-test` — aliases.

## Когда нужны настоящие сервисы

| Изменение | Обязательная проверка |
| --- | --- |
| SQL/cache/queue/OTel или migration recovery | `sh scripts/integration.sh` либо `./scripts/integration.ps1` |
| Basic HTML/CSRF/assets | `sh scripts/basic-e2e.sh` |
| Admin SQL authorization и browser persistence | `sh scripts/admin-e2e.sh` |
| REST auth/CRUD/error contract | `go test -race -tags e2e ./examples/rest-api` на Linux |
| Guide code, links, generator quickstart | `go run ./tools/checkdocs -run` |

Docker helpers создают отдельные Compose projects без host ports и удаляют только
свои containers/volumes; не передавайте им production credentials. Отсутствие
сервиса у обязательной integration suite — fail. Подробные cleanup/deadline границы
и coverage policy — в [testing contract](../testing.md). Полный lint debt и negative
coverage-script checks остаются в A4/12; docs gate их не подменяет.
