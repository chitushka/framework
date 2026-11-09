# Приёмка этапов 8 и 9 — 09.11.2026

Пункты 8.1–8.4 и 9.1–9.3 реализованы и проверены в baseline проекта.
Oracle/SQLServer остаются экспериментальными dialects без runtime conformance;
закрытие этих этапов не является сертификацией всего проекта как production-ready.

| Пункт | Реализация и результат | Доказательство |
| --- | --- | --- |
| 8.1 | PostgreSQL advisory/MySQL named lock на pinned connection; timeout/cancel/discard; transactional/nontransactional DDL, durable dirty marker и explicit resolve | [Unit recovery](../db/migration/recovery_test.go), [два процесса, kill, dirty recovery и concurrent index](../integration/migration_process_test.go), [CLI/операторский контракт](guide/migrations.md) |
| 8.2 | Placeholders/quoting, NULL/time/exact decimal, affected rows/IDs, pool exhaustion, активная отмена, savepoints, commit/rollback после смерти соединения и различия DDL | [SQL conformance на PostgreSQL/MySQL](../integration/sql_conformance_test.go), [контракт](guide/sql-contract.md) |
| 8.3 | Optimistic CAS/overflow без ранней мутации version, optional validation, composite keys, lazy/eager relations, pagination, один query вместо N+1, outer rollback | [Real AR conformance](../integration/ar_conformance_test.go), [unit regressions](../db/ar/hardening_test.go), [контракт](guide/active-record.md) |
| 8.4 | Generated model/CRUD компилируются и проходят tests в чистом Go-модуле; generated SQL действительно выполняется; metadata сохраняет nullable/precision; exclusive write и read-only dry-run/diff | [Baseline schema/project tests](../integration/generator_conformance_test.go), [CLI preview](../console/preview_test.go), [no-overwrite/diff](../gii/preview_test.go), [контракт](guide/generators.md) |
| 9.1 | Memory budgets/expiry, local stampede coalescing, wire/value limits, TLS/mTLS/auth и cancellation/reconnect; shared Redis session CAS/rotation и limiter | [Memory/concurrency](../cache/operational_test.go), [real mTLS/auth/outage/atomics](../integration/cache_operational_test.go), [контракт](guide/cache.md) |
| 9.2 | Готовые go-redis/amqp091 adapters, SQL CAS claim, leases/fencing/reclaim, quorum persistent confirms и at-least-once DLX, malformed quarantine | [SQL process/SDK lifecycle](../integration/queue_conformance_test.go), [outage/reconnect](../integration/queue_outage_test.go), [исчезновение DLX target](../integration/rabbit_dlx_test.go), [SDK module](../adapters/README.md) |
| 9.3 | Job/settlement budgets, panic recovery, backoff/jitter, durable final record до Ack, replay, abandon без расходования retry budget, drain/fatal cancellation и запрет пересечения поколений | [Dead-letter/drain](../queue/deadletter_test.go), [lease/storage/callback/fatal regressions](../queue/lifecycle_regression_test.go), [consumer kill и crash между Save/Ack](../integration/worker_crash_test.go), [idempotency/outbox contract](guide/queues.md) |

## Итоговые проверки

- Windows: `go vet ./...`, `go test -count=1 -timeout=2m ./...`; отдельно vet/tests adapters — пройдены.
- Linux: vet и `go test -mod=readonly -race -count=1 -timeout=2m ./...` ядра и adapters — пройдены.
- Полный integration suite с `-mod=readonly -race -count=1 -timeout=6m` на PostgreSQL,
  MySQL, Redis, Memcached и RabbitMQ — пройден. После разделения process test entry
  points повторный kill/restart suite также прошёл.
- Повторные queue tests: 5 прогонов на Windows; crash/recovery Redis/RabbitMQ
  повторён 3 раза после согласования lease с полным job/settlement budget.
- Coverage gate: **84,3% ядра** при пороге 80%; cache 80,4%, migration 80,5%,
  queue 81,8%. SDK acceptance выполняется integration-модулем.
- Golangci-lint v2.13.0: изменения относительно `885156c` — 0 замечаний;
  полный новый adapters module и новые integration изменения — 0 замечаний.
  Ранее известный полный lint debt ядра остаётся в 12.1; весь CI зелёным не объявляется.

CI integration использует тот же Compose baseline; SDK vet/race добавлены в workflow.
Исходники и go.mod/go.sum отдельного adapters module включены в integration image.
Тестовый RabbitMQ имеет internal DLX publisher-confirm retry interval 1s для быстрого
outage сценария; это явно тестовая настройка, не production default.

## Обязательная настройка приложения

- Для failed jobs задайте `WorkerConfig.DeadLetters`, например FileDeadLetters на
  persistent volume. Без store или при ошибке Save worker не подтверждает job и
  сообщает fatal error. Настройте retention/monitoring, закройте store/clients при shutdown.
- JobTimeout=1m, SettlementTimeout=5s, ShutdownTimeout=10s; default drain — до 1s.
  Lease должен превышать полное время обработки/settlement. Отмена кооперативная.
- Новые generated decimal/numeric поля — string; изменение JSON API существующего
  приложения требует явной миграции. Существующие модели автоматически не переписываются.
- RabbitMQ adapter создаёт quorum queues с reject-publish и 64 MiB на queue по умолчанию.
  Для прежних classic queues выберите новое имя/процедуру миграции. Redis требует
  AOF/noeviction и подходящей storage policy для сохранности при restart сервера.
- At-least-once допускает дубликаты; применяйте business idempotency и transactional outbox.
  Replay сохраняет исходную audit record. Memcached text protocol не имеет SASL/cross-key CAS;
  для authentication используйте TLS/mTLS deployment, для shared sessions — Redis adapter.

Baseline доказывает process/transport recovery. Потеря host/диска, multi-node quorum
failover, cluster/sentinel и прикладные authentication flows требуют своей приёмки.
Межпроцессный stampede control не подменяется local GetOrLoad. Этапы 10–13 остаются открыты.
