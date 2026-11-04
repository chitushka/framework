# Реестр публичных гарантий

Аудит этапа 6 охватывает README, все главы Guide, REQUIREMENTS и exported API
библиотечных пакетов. REQUIREMENTS — целевой SRS, а не перечень готовых гарантий.
Статус поддержки определяется [контрактом](support.md); тесты ниже доказывают только
указанный сценарий. Fake SQL driver, TCP fixture и httptest не доказывают production
совместимость, сетевой streaming или устойчивость к падению процесса.

| Заявлено / API | Реализовано | Проверено / явное ограничение |
| --- | --- | --- |
| Lifecycle `app.Application`, modules, rollback | Register/Boot/Shutdown, runtime ErrorSource | [app tests](../../app/application_test.go); зависший module и process SIGTERM budget — этап 10.2 |
| DI `di.Container`, component.Registry | Constructors, singleton/transient, bindings, cycle detection | [DI](../../di/container_test.go), [components](../../component/registry_test.go); зависимости должны быть concurrency-safe |
| `event.Bus`, behavior decorators | Priority, unsubscribe, concurrent publish, typed decorators | [events](../../event/bus_test.go), [behaviors](../../behavior/behavior_test.go); нет durable event bus |
| Config JSON/.env/env/defaults/required | Merge и typed decode | [config tests](../../config/loader_test.go); secret manager предоставляет приложение |
| Router, controller hooks, DI, URL generation | Static/parameter/host routes, groups, named URLs | [router](../../web/router_test.go), [controllers](../../web/controller_test.go); proxy trust и полная HEAD/OPTIONS/encoded-path conformance — этап 7 |
| BindJSON / responses | Один JSON value, неизвестные поля отклоняются; HTML/XML/file/stream | [binding](../../web/router_test.go), [responses](../../web/context_response_test.go); body limit/media-type policy не предоставляется, JSON value не обязан быть объектом |
| HTTP server / graceful shutdown | http.Server lifecycle и явные timeout options | [server](../../web/server_test.go); нет безопасного полного timeout профиля по умолчанию, streaming wrappers проверены не во всех сочетаниях |
| Views, widgets, Vue state, assets | html/template, JSON state escaping, Vite manifest | [views](../../web/view_test.go), [assets](../../asset/asset_test.go); template.HTML и произвольные URLs требуют доверенного ввода |
| SQL Connection и dialects | database/sql pool, PostgreSQL/MySQL/Oracle/SQLServer dialects | [connection](../../db/connection_test.go), [dialects](../../db/dialect_test.go), [real SQL baseline](../../integration/services_test.go); Oracle/SQLServer только unit/fakes, drivers подключает приложение |
| Transactions/savepoints | Callback commit/rollback, nested savepoints, panic cleanup | [transactions](../../db/transaction_test.go), [baseline](../../integration/services_test.go); DDL зависит от СУБД, full cancellation/commit-failure conformance — этап 8.2 |
| Query Builder / SQL injection protection | Placeholders для значений, quoting identifiers | [query](../../db/query_test.go), [baseline](../../integration/services_test.go); Raw/RawCondition не sanitise SQL, имена выбираются из allowlist |
| Migrations / CLI | Version/checksum/history, последовательные up/down | [runner](../../db/migration/runner_test.go), [CLI](../../db/migration/cli_test.go), [baseline](../../integration/services_test.go); **нет межпроцессного lock/dirty recovery**, MySQL DDL не откатывается атомарно |
| Active Record / relations / optimistic lock | Metadata, CRUD, generated IDs, eager/lazy relations, version check | [repository](../../db/ar/repository_test.go); fake driver, реальная конкурентная запись/relations — этап 8.3 |
| Validation / mass assignment | Rules/scenarios, conversion перед изменением модели | [validation](../../validation/validation_test.go), [assignment](../../validation/assignment_test.go); unique validator не заменяет constraint БД |
| Cache Memory/Redis/Memcached/SQL, TTL/tags/dependencies | Common API, namespace generations, short-lived TCP | [cache](../../cache/cache_test.go), [network fixtures](../../cache/network_test.go), [real backends](../../integration/services_test.go); нет CAS/stampede/TLS; Go dependencies process-local, чужая запись считается stale; concurrent tag changes не линейризуемы |
| Sessions, Identity/User, CSRF, headers | Session storage, regenerate ID, auth middleware, CSRF primitives | [session](../../session/session_test.go), [auth](../../auth/user_test.go), [security](../../security/security_test.go); нет session CAS/absolute TTL/key rotation доказательств, production password/token verifier не поставляется |
| RBAC hierarchy/rules/SQL store | Inheritance, assignments/revoke, contextual rules | [RBAC](../../rbac/rbac_test.go), [SQL fake](../../rbac/database_test.go); real SQL store/multi-process invalidation не входят в baseline |
| Queue API, worker, Memory/SQL/Redis/RabbitMQ | JSON jobs, polling, retries, leases API, adapter interfaces | [worker/adapters](../../queue/queue_test.go), [SQL fake](../../queue/database_test.go), [SQL smoke](../../integration/services_test.go); нет Redis/AMQP SDK adapters, crash/durability доказательств и durable dead-letter; final failure Ack удаляет job |
| Worker drain / deadlines | Shutdown прекращает reserve и ждёт handlers | [worker](../../queue/queue_test.go); handler обязан учитывать context; settlement без собственного deadline, per-job timeout задаёт приложение |
| I18n | Locale middleware, catalogs, fallback/plural, escaped templates | [i18n](../../i18n/i18n_test.go); только реализованные plural families, не полная ICU/CLDR совместимость |
| Console, Gii model/migration/CRUD | CLI dispatch, schema inspection, generated files, no-overwrite | [console](../../console/tools_test.go), [Gii](../../gii/crud_test.go), [model](../../db/ar/generator_test.go); compilation generated projects/real schema matrix — этап 8.4 |
| Vue 3/Vite/AdminKit scaffold | Source, lockfile, manifest loader | [frontend](../../frontend/package.json), [assets](../../asset/asset_test.go); browser E2E/frontend CI gate пока отсутствуют |
| Logging | slog handlers, fanout, file/console | [logging](../../logging/logging_test.go); DB/Syslog/Elasticsearch/Telegram handlers реализует приложение |
| Metrics/tracing/debug | Prometheus text, W3C HTTP traceparent, SpanExporter, opt-in toolbar | [observability](../../observability/observability_test.go), [debug](../../debug/toolbar_test.go); exporter синхронный, ошибки игнорируются; нет OTel adapter и автоматической SQL/queue propagation, redaction только явная |
| Basic/Advanced Admin/REST | Запускаемые учебные HTTP handlers | [examples](../../examples/README.md), их main_test.go; Basic fixed credentials/CSS hash, Admin username-only и memory stores, REST handwritten JWT/fallback secret — не production auth |
| CI и coverage ≥80% | Unit/race/vet/lint/coverage, baseline OS и integration jobs | [workflow](../../.github/workflows/ci.yml), [script](../../scripts/check-coverage.sh); coverage только statement агрегат, не доказательство security и не автоматическая branch protection |

## Воспроизведение из чистого checkout

Локальная проверка этапа 6 (04.11.2026): Windows unit/vet, Linux unit/race/vet,
coverage 83,1%, actionlint, обе команды интеграционного стенда и проверка Markdown links
прошли. Отсутствующая конфигурация интеграционного теста подтверждённо вызывает fail.
Полный `golangci-lint v2.13.0 run` выявил **89 существующих замечаний** (61 errcheck,
28 staticcheck), включая непроверенные ошибки Close/CLI output и намеренные nil contexts
в негативных тестах. Поэтому утверждение «весь CI зелёный» не подтверждено; lint gate
сохраняется обязательным, устранение накопленных замечаний относится к этапу 12.1.
Отдельный lint модуля `integration` и `golangci-lint run --new-from-rev HEAD` для изменений
этапа 6 прошли без замечаний; это не заменяет полный lint gate.
Удалённый workflow и настройка branch protection локальным запуском не подтверждаются.

Для автономных unit tests: `docker build --target test -t chitu-test .`.
Для обязательного real-service baseline: `sh scripts/integration.sh` (PowerShell:
`./scripts/integration.ps1`). Нужен Docker Engine с Linux containers и Compose v2;
сервисы/драйверы загружаются при первом запуске. Подробности — [testing](testing.md).
Демонстрации запускаются через `go run ./examples/basic`, `go run ./examples/advanced-admin`
и `go run ./examples/rest-api`, как описано в [examples](../../examples/README.md).
Для frontend нужен отдельный `npm ci && npm run build` из `frontend`.
Проверка frontend/browser и live CI results остаётся отдельной от Go baseline.
