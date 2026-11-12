# Реестр публичных гарантий

Аудит этапа 6 охватывает README, все главы Guide, REQUIREMENTS и exported API
библиотечных пакетов. REQUIREMENTS — целевой SRS, а не перечень готовых гарантий.
Статус поддержки определяется [контрактом](support.md); тесты ниже доказывают только
указанный сценарий. Fake SQL driver, TCP fixture и httptest не доказывают production
совместимость, сетевой streaming или устойчивость к падению процесса.

| Заявлено / API | Реализовано | Проверено / явное ограничение |
| --- | --- | --- |
| Lifecycle `app.Application`, modules, rollback / health | Shared finite shutdown/rollback budget, quiesce/reverse drain/finalizers, SIGTERM, bounded liveness/readiness probes | [app](../../app/lifecycle_test.go), [health](../../web/health_test.go), [Linux process + Redis/PostgreSQL/collector](../../integration/lifecycle_process_test.go), [контракт](lifecycle.md); некооперативная goroutine живёт до выхода процесса, module с неуспешным Boot сам отвечает за частичные ресурсы |
| DI `di.Container`, component.Registry | Constructors, singleton/transient, bindings, cycle detection | [DI](../../di/container_test.go), [components](../../component/registry_test.go); зависимости должны быть concurrency-safe |
| `event.Bus`, behavior decorators | Priority, unsubscribe, concurrent publish, typed decorators | [events](../../event/bus_test.go), [behaviors](../../behavior/behavior_test.go); нет durable event bus |
| Config JSON/.env/env/defaults/required | Merge и typed decode | [config tests](../../config/loader_test.go); secret manager предоставляет приложение |
| Router, controller hooks, DI, URL generation | Static/parameter/host routes, groups, named URLs, HEAD fallback/OPTIONS/Allow, segment-wise escaping | [router](../../web/router_test.go), [controllers](../../web/controller_test.go), [network conformance](../../web/http_conformance_test.go); строгая path/trailing slash policy описана в [contract](http-compatibility.md) |
| Proxy trust / host / redirect / CORS | Explicit CIDR и header family, правый доверенный suffix chain, host/origin allowlists, preflight/Vary/credentials | [proxy, включая TCP](../../web/proxy_test.go), [CORS](../../web/cors_test.go), [сценарии и ограничения](proxy.md); allowlists задаёт приложение, raw headers не являются доверенными |
| BindJSON / responses | Один JSON value, неизвестные поля отклоняются, JSON Content-Type обязателен; HTML/XML/file/stream | [binding](../../web/router_test.go), [responses](../../web/context_response_test.go), [limits](../../web/limits_test.go); вручную созданный Context требует внешнего body limiter |
| HTTP resources / graceful shutdown | Body/form/upload limits, multipart cleanup, конечные timeouts/header budget, Streaming и writer capabilities | [server](../../web/server_test.go), [policy](../../web/server_policy_test.go), [limits](../../web/limits_test.go), [network writer chain](../../web/writer_chain_test.go); metadata после bounded parsing, сторонние wrappers требуют своей проверки |
| File/Download / response state | ServeContent Range/preconditions, explicit ETag, flush/cancellation, tracking direct writes; FileFromRoot/DownloadFromRoot через os.Root | [network files](../../web/http_conformance_test.go), [writer/error chain](../../web/writer_chain_test.go), [rooted/network security tests](../../web/security_audit_test.go); обычный File принимает trusted path, source Read учитывает cancellation, hijacked socket принадлежит caller |
| Views, widgets, Vue state, assets | html/template, JSON state escaping, Vite manifest | [views](../../web/view_test.go), [assets](../../asset/asset_test.go); template.HTML и произвольные URLs требуют доверенного ввода |
| SQL Connection и dialects | database/sql pool, PostgreSQL/MySQL/Oracle/SQLServer dialects | [connection](../../db/connection_test.go), [dialects](../../db/dialect_test.go), [real SQL baseline](../../integration/services_test.go); Oracle/SQLServer только unit/fakes, drivers подключает приложение |
| Transactions/savepoints | Callback commit/rollback, nested savepoints, panic cleanup | [transactions](../../db/transaction_test.go), [real conformance](../../integration/sql_conformance_test.go), [dialect differences](sql-contract.md); commit при сетевом отказе может быть неопределённым, MySQL DDL имеет implicit commit |
| Query Builder / SQL injection protection | Placeholders для значений, quoting identifiers | [query](../../db/query_test.go), [baseline](../../integration/services_test.go); Raw/RawCondition не sanitise SQL, имена выбираются из allowlist |
| Migrations / CLI | Session advisory/GET_LOCK на pinned connection, checksum/history, dirty state и явный resolve; PostgreSQL transactional/nontransactional DDL и MySQL implicit commits | [unit recovery](../../db/migration/recovery_test.go), [process/crash](../../integration/migration_process_test.go), [операторский контракт](migrations.md); Oracle/SQLServer Runner отклоняет, disk/host crash вне доказательств |
| Active Record / relations / optimistic lock | Version CAS без ранней мутации модели, overflow guard, optional validation, composite keys, eager/lazy relations | [real concurrency/relations/pagination/rollback](../../integration/ar_conformance_test.go), [контракт](active-record.md); отдельная модель на writer, transaction задаёт application service |
| Validation / mass assignment | Rules/scenarios, conversion перед изменением модели | [validation](../../validation/validation_test.go), [assignment](../../validation/assignment_test.go); unique validator не заменяет constraint БД |
| Cache Memory/Redis/Memcached/SQL, TTL/tags/dependencies | Memory budgets/expiry, local GetOrLoad coalescing, TLS/auth/I/O cancellation; SDK Redis CAS/rotation и shared limiter | [memory/load](../../cache/operational_test.go), [real TLS/outage/atomics](../../integration/cache_operational_test.go), [контракт](cache.md); Memcached без SASL/cross-key CAS, distributed stampede и cluster вне baseline |
| Sessions, Identity/User, CSRF, headers | AtomicBackend CAS/rotation с 409, idle/absolute TTL, bounded cookie key ring, body/header CSRF | [две HTTP instances](../../session/concurrency_test.go), [TTL/rotation/revocation/errors](../../session/lifetime_test.go), [CSRF/login/logout](../../security/csrf_contract_test.go), [контракт и миграция](sessions.md); [Redis SDK backend](cache.md) проверен на общем CAS/rotation; application session flows требуют своих интеграционных сценариев, stateless CookieStore не имеет индивидуального revocation |
| Passwords/credentials/throttling/JWT | Argon2id PHC с cost/concurrency limits и CAS rehash; account/client fixed-window limiter; public-key JWT verifier и bearer transport | [passwords](../../auth/password_test.go), [credentials/limiter](../../auth/credentials_test.go), [JWT/rotation](../../auth/jwt_test.go), [Bearer](../../auth/token_test.go), [контракт](authentication.md); CredentialStore/общий limiter/identity provider/revocation предоставляет приложение |
| RBAC hierarchy/rules/SQL store | Inheritance, assignments/revoke, contextual rules | [RBAC](../../rbac/rbac_test.go), [SQL fake](../../rbac/database_test.go); real SQL store/multi-process invalidation не входят в baseline |
| Queue API, worker, Memory/SQL/Redis/RabbitMQ | Готовые Redis/RabbitMQ SDK adapters в отдельном модуле, lease fencing/reclaim, confirms, SQL CAS, malformed quarantine | [real processes/leases](../../integration/queue_conformance_test.go), [outage](../../integration/queue_outage_test.go), [kill/restart](../../integration/worker_crash_test.go), [контракт](queues.md); at-least-once, AMQP publish+ack допускает дубликаты, disk/host durability зависит от deployment |
| Worker cancellation / deadlines / panic | Job/settlement budgets, panic recovery, backoff/jitter, durable dead-letter до Ack, replay, bounded drain и запрет пересечения поколений | [dead-letter/lifecycle](../../queue/deadletter_test.go), [fatal/lease regressions](../../queue/lifecycle_regression_test.go), [Save/Ack crash](../../integration/worker_crash_test.go); hooks/handlers должны завершаться кооперативно, FileDeadLetters требует persistent volume |
| I18n | Locale middleware, catalogs, fallback/plural, escaped templates | [i18n](../../i18n/i18n_test.go); только реализованные plural families, не полная ICU/CLDR совместимость |
| Console, Gii model/migration/CRUD | CLI dispatch, schema metadata, exclusive generated files, dry-run/diff; decimal как string, nullable pointers | [generated project compilation/tests и реальный DDL](../../integration/generator_conformance_test.go), [preview/no-overwrite](../../gii/preview_test.go), [контракт](generators.md); каталоги вывода должны принадлежать приложению |
| Vue 3/Vite/AdminKit scaffold | Source, lockfile, manifest loader | [frontend](../../frontend/package.json), [assets](../../asset/asset_test.go); browser E2E/frontend CI gate пока отсутствуют |
| Logging | slog handlers, fanout, file/console | [logging](../../logging/logging_test.go); DB/Syslog/Elasticsearch/Telegram handlers реализует приложение |
| Metrics/tracing/debug / production profile | Finite method/status labels, Prometheus parser, SDK OTLP HTTP, HTTP/SQL/queue propagation, parent-based sampling, bounded batching/flush/stats; production profile отключает diagnostics | [SDK sampling/privacy/limits](../../adapters/otel/otel_test.go), [outage/cleanup](../../adapters/otel/outage_test.go), [collector/process/retry/Prometheus](../../integration/observability_test.go), [legacy limits](../../observability/export_test.go), [production](../../web/security_audit_test.go), [контракт/миграция](observability.md); SQL spans ограничены вызовами Connection/Transaction до rows/Scan, baggage/tracestate не передаются, export best-effort; application exporters должны соблюдать context |
| Basic/Advanced Admin/REST | Запускаемые учебные HTTP handlers; REST использует общий verifier внешних Ed25519 tokens без встроенного issuer/secret | [examples](../../examples/README.md), их main_test.go; Basic fixed credentials/CSS hash, Admin username-only и memory stores; полная production приёмка starters — этап 11 |
| CI и coverage ≥80% | Unit/race/vet/lint/coverage, baseline OS и integration jobs | [workflow](../../.github/workflows/ci.yml), [script](../../scripts/check-coverage.sh); coverage только statement агрегат, не доказательство security и не автоматическая branch protection |

## Воспроизведение из чистого checkout

Шаг 7.2: Windows unit/vet и Linux vet/race/coverage прошли; покрытие ядра 84,0%,
lint diff — 0 замечаний. Проверены spoofing от недоверенного peer, trusted chains,
IPv6, Host routing/allowlists, redirect destinations и CORS preflight/Vary/credentials.
Таблица сценариев и строгие ограничения parser находятся в [proxy contract](proxy.md).

Шаг 7.1: Windows unit/vet, Linux race и coverage gate прошли (покрытие ядра 83,4%).
Новые сетевые tests повторены пять раз; lint diff шага, включая новые файлы, — 0 замечаний.
Проверены chunked overflow, header/body/write/idle budgets, streaming disconnect и cleanup
multipart при success/error/panic/обрыве загрузки. Цепочка writer wrappers проверена отдельно в шаге 7.3.

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
