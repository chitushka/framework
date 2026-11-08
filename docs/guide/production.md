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

Handler задачи должен быть идемпотентным: broker может доставить сообщение повторно.
В `WorkerConfig` задаются `JobTimeout` (по умолчанию 1 минута) и `SettlementTimeout`
(по умолчанию 5 секунд); отрицательные значения отклоняются. Для долгих задач явно
увеличьте `JobTimeout` и настройте lease broker так, чтобы он превышал время обработки
и settlement с запасом. Внешнее хранение финальных ошибок задаёт приложение:
durable dead-letter/replay ещё не поставляются, после финальной ошибки worker вызывает Ack. Redis/RabbitMQ требуют
реализации SDK adapters приложением. В payload передавайте идентификаторы, а не большие изменяемые снимки модели.

Worker реализован как модуль приложения: shutdown отменяет context обработки, прекращает
цикл reserve и ожидает handlers в пределах `ShutdownTimeout`. Handler должен учитывать
context; deadline не прерывает произвольный Go-код и не создаёт отдельную goroutine для
принудительного завершения. Panic handler преобразуется в `*queue.PanicError` и проходит
обычную политику retry/final failure. Возврат nil после истечения `JobTimeout` также
считается ошибкой deadline. Успешный handler, вернувший nil при shutdown, подтверждается.

Ack/Nack получают независимый context с конечным `SettlementTimeout`, даже после
отмены обработки; broker обязан учитывать этот context. Ошибка settlement передаётся
через `Worker.Errors()`. `OnFailure` и `RetryDelay` вызываются синхронно: они должны
быстро возвращать управление и не вызывать panic. Crash/restart, restart во время
shutdown и общий process shutdown budget пока не подтверждены.

Повторный Nack после ошибки Redis/AMQP использует тот же следующий номер попытки.
AMQP publish с последующей ошибкой Ack всё ещё может создать дубликат: идемпотентность
обязательна, атомарность publish+ack не гарантируется.

## Observability

Приложение должно добавлять безопасные бизнес-идентификаторы в структурированные логи.
HTTP middleware записывает route и статус; не помещайте user ID или произвольные URLs в labels.
Tracer поддерживает HTTP traceparent и SpanExporter; автоматической связи SQL/queue spans
и готового OpenTelemetry adapter пока нет. Export синхронный, ошибка exporter игнорируется:
latency/ограничение памяти при сбоях должен контролировать adapter приложения.

Debug toolbar и подробные ошибки отключайте в production. Health endpoint должен различать liveness и readiness: readiness проверяет способность обслуживать новый запрос.

## Завершение и развёртывание

При SIGTERM прекратите принимать запросы, завершите handlers и workers, затем закройте соединения. Shutdown timeout должен быть меньше grace period платформы. Миграции запускайте отдельным release job до переключения трафика.
