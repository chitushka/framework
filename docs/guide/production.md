# Production

Это рекомендации по проектированию приложения. Текущие ограничения, модель угроз,
TLS reverse proxy deployment и границы ответственности определяет [контракт поддержки](support.md).
Наличие API не подтверждает production readiness.

## Конфигурация

Оборачивайте публичную HTTP chain в `web.NewProductionHandler`, а для логов
используйте `logging.NewProductionHandler`. Профиль отключает framework toolbar/pprof,
скрывает framework error/panic details и raw paths в стандартной observability.
Порядок middleware, ограничения redaction, rooted file API и threat-model checklist —
в [security audit 7.6](security-audit.md). Это явная настройка, без переключения через client headers.

Слои применяются от общих к специфичным: JSON, `.env`, системное окружение. После merge конфигурация декодируется в типизированную структуру, заполняет defaults и проверяет required-поля. В production не полагайтесь на `.env`; окружение процесса или secret manager имеют более ясную модель доступа.

## Cache

Выбирайте backend по согласованности и масштабу: memory — один процесс, Redis/Memcached — распределённый cache, database — когда отдельная инфраструктура нежелательна. Ключи должны включать версию схемы и tenant. TTL обязателен для производных данных.

Cache-aside требует явной инвалидации после успешной транзакции. Cache не является источником истины.

## Queues

Готовые SDK adapters, настройки leases/confirmations, durable dead-letter/replay,
drain и transactional outbox описаны в [контракте очередей](queues.md).
Финальная ошибка сохраняется до Ack; WorkerConfig.DeadLetters обязателен для
безопасного завершения failed jobs. Настройте persistent volume и наблюдайте Errors.
Default JobTimeout=1m, SettlementTimeout=5s, DrainTimeout=1s при ShutdownTimeout=10s.
Handlers должны учитывать context, а broker lease превышать полный бюджет обработки.
At-least-once требует идемпотентности; AMQP retry publish+Ack может дать дубликаты.

## Observability

Приложение должно добавлять безопасные бизнес-идентификаторы в структурированные логи.
HTTP middleware записывает статус; не помещайте user ID или произвольные URLs в labels.
Отдельный `adapters/otel` поставляет SDK/OTLP HTTP, HTTP/SQL/queue propagation,
parent-based sampling и bounded batching. Legacy tracer тоже экспортирует асинхронно.
Наблюдайте export statistics и вызывайте Shutdown после HTTP/worker drain с независимым
cleanup context. Defaults, redaction, ограничения SQL instrumentation и flush budgets —
в [контракте observability](observability.md).

Debug toolbar и подробные ошибки отключайте в production. Health endpoint должен различать liveness и readiness: readiness проверяет способность обслуживать новый запрос.

## Завершение и развёртывание

Используйте `app.SignalContext` для interrupt/SIGTERM и задавайте общий budget через
`Application.SetShutdownTimeout` до Start (default 15s). Readiness снимается перед
stop hooks, workers прекращают reserve в quiesce, modules останавливаются в обратном
порядке; exporter finalizer добавляется до HTTP/worker modules. Независимый cleanup
context и общий deadline ограничивают rollback, drain и flush. Health API, точный
порядок, composition example и ограничения зависших callbacks — в
[lifecycle contract](lifecycle.md). Grace period платформы должен превышать этот
budget. Миграции запускайте отдельным release job до переключения трафика.
