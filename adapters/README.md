# SDK adapters

Это отдельный Go-модуль: ядро framework не получает обязательных Redis/AMQP/OTel SDK.
Для локальной разработки module replacements заданы в integration/go.mod;
приложение подключает adapters и framework совместимых версий.

- `redis.NewClient(*redis.Options)` задаёт finite context/I/O budgets и отключает
  автоматический replay неопределённых операций. `redis.New(client, namespace)`
  реализует cache Backend, session CAS и queue.RedisStore; `NewLimiter` — общий
  auth.AttemptLimiter. Lua transitions используют receipt fencing и server time.
- `rabbitmq.New(Config)` реализует queue.RabbitChannel: quorum queues, persistent
  publish confirms, reject-publish при MaxQueueBytes (64 MiB default), at-least-once
  DLX, delayed routing, malformed quarantine и reconnect отдельными соединениями.
  Установите TLSConfig для amqps и закрывайте Channel при shutdown.
- `otel.NewHTTP(ctx, Config, HTTPConfig)` подключает OpenTelemetry SDK/OTLP HTTP
  для HTTP/SQL/queue без глобального provider. Явные TLS/auth/endpoint, sampling,
  bounded nonblocking batching, Stats/Flush/Shutdown; детали и миграция legacy
  tracer — в [observability](../docs/guide/observability.md).

Владелец Redis client вызывает Close. Для Redis durability настройте AOF/noeviction,
для RabbitMQ — quorum availability и persistent storage. At-least-once допускает
дубликаты; broker adapter не заменяет idempotency/outbox application service.

[Cache contract](../docs/guide/cache.md), [queue contract](../docs/guide/queues.md),
[real conformance](../integration/queue_conformance_test.go),
[outage](../integration/queue_outage_test.go), [DLX target outage](../integration/rabbit_dlx_test.go).

Проверки модуля: `go vet ./...`, `go test -race ./...` из adapters.
Полная приёмка SDK находится в integration и запускается scripts/integration.sh
либо scripts/integration.ps1 на изолированных сервисах.
