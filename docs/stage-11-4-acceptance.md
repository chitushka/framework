# Приёмка 11.4 — руководство и onboarding

Дата: 13.11.2026. [Onboarding](guide/getting-started.md),
[cookbook](guide/cookbook/README.md), [generator quickstart](guide/generator-quickstart.md),
[API migration guide](guide/api-migration.md).

## Реализация

Cookbook содержит отдельные deployment/testing/security/migrations/queues recipes:
выбор starter/state model, TLS proxy/config, health/shutdown budgets, test matrix,
trust/auth/mass assignment, release migration/dirty recovery и worker/DLQ/replay/outbox.
Guide index, root/examples README и контракты production/testing/generators связаны
с новыми маршрутами чтения. API migration guide сводит pre-v1 breaking changes,
schema/session/key rollout и изменения Basic/Admin/REST, без обещания generic
автоматического upgrade для любого старого commit.

Три полных executable snippets находятся в `examples/guide`:

- HTTP: loopback JSON service, production wrapper, отдельные liveness/readiness,
  Application/HTTP shutdown budgets и SignalContext.
- Jobs: один MemoryBroker job, FileDeadLetters, typed handler, отмена и независимый
  cleanup context; пример явно не обещает durable jobs/deduplication.
- Generate: model/CRUD/search/input/Vue и PostgreSQL up/down из явной schema без SQL
  driver, decimal string и optimistic version. Generated controller компилируется
  и проходит отдельный input-policy test; повторная генерация не перезаписывает код.

`go run ./tools/checkdocs` проверяет repository Markdown links/heading anchors,
case и границы paths/symlinks, игнорирует code/dependency/build directories.
Fences `go file=...` должны точно совпадать с исходниками после CRLF normalization;
каждый executable guide main обязан иметь fence. Негативные tests отвергают missing
file/anchor/reference, malformed links, stale/unclosed fences, выход из repository,
пустое дерево и непредставленный в документации executable snippet.

`-run` создаёт отдельный временный module, копирует только guide sources/tests,
реально выполняет `go mod init/edit/tidy/test`, использует local framework replace,
отключает go.work и network resolution, затем удаляет временный каталог. Внутри
generator test создаётся ещё один чистый generated module. Test намеренно падающего
примера подтверждает передачу subprocess exit status. Общий budget 3 минуты,
generated compilation 2 минуты; subprocess WaitDelay ограничен.

Добавлены `make docs`/`make docs-test`, docs в `make ci`, независимый CI docs job с
JSON test artifacts и тот же isolated onboarding в Windows job. Production library
API и зависимости не менялись.

## Проверки

Из корня checkout:

```bash
go mod download
go vet ./...
go test ./...
go run ./tools/checkdocs -run
# Linux с CGO:
go test -race ./...
golangci-lint run ./internal/doccheck/... ./tools/checkdocs/... ./examples/guide/...
sh scripts/check-coverage.sh coverage.out 80
```

Windows vet/tests и isolated onboarding прошли. Linux full root race suite прошёл;
после добавления negative subprocess test и правки текста ошибки повторно проверены
затронутые packages. Linux использует source snapshot внутри локального Docker
image с prefetched pinned Go dependencies и `GOPROXY=off`, без repository build
outputs и пользовательского go.work. HTTP test открывает свободный loopback port;
guide tests не требуют Node, SQL, broker или secrets.

| Итоговый gate | Результат |
| --- | --- |
| Windows `go vet ./...`, `go test ./...` | Прошли |
| Windows `go run ./tools/checkdocs -run` | Прошёл; три packages внешнего module |
| Linux `go vet ./...`, full root `go test -race ./...` | Прошли |
| Итоговый Linux race затронутых checker/examples, включая failed-example regression | Прошёл |
| Linux isolated onboarding и compilation generated project | Прошли с отключённой network resolution |
| golangci-lint v2.13.0 нового checker/CLI/examples | 0 замечаний |
| Coverage gate | 84,6% при required 80%; checker package 86,5% |
| actionlint, gofmt и git diff check | Прошли |

После обновления плана/отчёта повторная проверка прошла: 48 Markdown documents,
472 links и 3 source fences. Прямые `go run` commands worker/generator также
успешно выполнены с отдельным output в `.cache/docs-smoke-20261113-11-4`.

## Границы

Автоматически исполняются полные source-backed guide snippets и новая module/
generator compilation. Старые короткие Go fragments в reference chapters не
выдаются за самостоятельные программы. Инструмент не исполняет shell commands
из Markdown. Deployment/SQL/browser recipes с вашими hosts/secrets/DSN требуют
соответствующего окружения; contracts и существующие CI suites указаны ссылками.
Новая приёмка не подменяет реальные service/browser acceptance этапов 8–11.3.

External HTTP(S)/mailto links проверяются только синтаксически, без fetch:
доступность/содержимое upstream страниц не подтверждаются. Markdown format и
исключаемые directories явно описаны в [docs gate contract](guide/cookbook/testing.md#проверка-документации).
Production deploy/backup/restore/SLO и общий production-ready v1.0 остаются для RC.

Полный прежний lint debt, negative coverage-script checks и остальные A4/12 gates
не закрыты. Удалённый CI и branch protection локальными прогонами не подтверждаются.
Следующий инкремент — 12.1: полный достоверный quality gate.
