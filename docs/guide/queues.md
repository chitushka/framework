# Очереди, retries и восстановление

Все persistent brokers дают at-least-once, не exactly-once. Обработчик обязан быть
идемпотентным (например, UNIQUE business operation ID в той же SQL-транзакции, что
и изменения). Attempt считает обработанные ошибки; восстановление lease после
смерти процесса может повторить тот же Attempt. Payload ограничен 16 MiB.

Для OpenTelemetry задайте observer в Queue/WorkerConfig из `adapters/otel`.
SQL broker требует `DatabaseConfig.TraceContext: true` и nullable traceparent/tracestate
columns; прежняя схема без tracing продолжает работать. Traced enqueue без opt-in
возвращает ошибку до INSERT. Миграция и propagation через retries/processes —
в [observability contract](observability.md).

## Готовые adapters

Модуль `github.com/chitushka/framework/adapters` отделён от стандартного ядра.
`adapters/redis.NewClient` и `redis.New(client, namespace)` дают готовый RedisStore.
Подключите его в queue.NewRedisBroker. Scripts атомарно выполняют push, reserve,
retry и Ack; leases имеют случайный owner token и используют Redis server time.
При истечении lease reserve переносит до 100 orphan jobs за вызов обратно в ready.
Старый owner не может подтвердить новую доставку. Настройте ReservationTimeout
больше JobTimeout + SettlementTimeout + запас, синхронизируйте часы producers.
Renewal lease не выполняется: задачи должны укладываться в выбранный предел.
Для сохранности при restart Redis включите AOF/appendfsync и noeviction, backups;
успешный RESP reply сам по себе не доказывает fsync на storage платформы.

`adapters/rabbitmq.New(Config{URL, TLSConfig, OperationTimeout})` реализует
RabbitChannel на официальном SDK. Он объявляет durable queue, публикует persistent
messages с mandatory routing и ждёт publisher confirm. Failed publish возвращает
ошибку, автоматического replay при неопределённом результате нет. Каждая publish/
reservation использует собственное соединение; следующая операция reconnects.
Это ограничивает совместное состояние, но не является high-throughput pooled adapter.
Закрывайте adapter при остановке: Close возвращает unacked jobs серверу.

Все три AMQP queues — quorum, с reject-publish и MaxQueueBytes=64 MiB по умолчанию.
Main/delay включают at-least-once DLX: source удерживает сообщение до confirm цели,
в том числе при её временной недоступности. Старые classic queues требуют нового
имени/миграции, тип существующей queue не меняется декларацией. Framework управляет
retry budget; broker delivery-limit отключён, crash redelivery не удаляет job.

Delayed AMQP jobs используют durable `.delay` queue с message TTL и DLX в основную
queue, без plugin. Из-за FIFO mixed TTL короткая задержка может ждать длинную впереди:
гарантируется не раньше delay, точное расписание не обещается. Максимум delay 24h.
Publish retry-envelope + Ack original не атомарны: возможны дубликаты, потерь из-за
Ack до подтверждённой публикации нет. SDK операции закрывают socket при deadline/
cancel: один PublishWithContext не является достаточной гарантией отмены.

Malformed Redis envelopes атомарно перемещаются в quarantine; InvalidMessages даёт
bounded read, Purge удаляет queue/quarantine после остановки consumers. Legacy RedisStore
без Quarantine сохраняет malformed сообщение под lease и сообщает ошибку. RabbitMQ
Reject(false) направляется в durable `.invalid` queue. Исправляйте malformed payload
вручную; не запускайте автоматический replay непроверенных bytes.

SQL broker выполняет CAS lease вместе с выбранными Attempt/available_at. Это закрывает
гонку SELECT/reserve/Nack. Owner token ограничивает Ack/retry/abandon; expired lease
может быть забран другим процессом. MemoryBroker также reclaim-ит unacked deliveries,
но хранит всё только в памяти процесса и не заменяет durable broker.

## Worker и dead-letter

Defaults: JobTimeout=1m, SettlementTimeout=5s, ShutdownTimeout=10s,
DrainTimeout=min(1s, половина shutdown budget). Handler и backend должны учитывать
context. Panic handler превращается в PanicError и обычную failed attempt.
Default retry — exponential backoff с jitter, от 0,5s до 1m; custom RetryDelay
ограничивается 0..24h. Hooks синхронные и должны быстро завершаться. Panic hook
передаётся в Errors и останавливает всю consumer group.

Финальная ошибка **сохраняется до Ack** в WorkerConfig.DeadLetters. Если store не
задан или Save неуспешен, worker сообщает fatal error и не подтверждает job.
Готовые deliveries поддерживают Abandon: инфраструктурная ошибка возвращает job
без расходования handler retry budget. Если abandon не удался, persistent lease/
соединение остаётся способом восстановления. Всегда наблюдайте Worker.Errors либо
запускайте worker как app.Module через Application.Run.

NewFileDeadLetters(directory) хранит atomic JSON records, fsync-ит файл и Unix directory,
безопасно повторяет Save по message ID. Используйте persistent volume с закрытыми
правами, задайте retention/monitoring и закройте store при shutdown. На Windows
namespace durability зависит от файловой системы. Shared replicas требуют общей
durable FS или своей реализации DeadLetterStore, а не отдельных container layers.
Disk-full/permission ошибки fail closed. Errors могут содержать application data:
redaction и доступ к файлам задаёт приложение.

List/Get доступны оператору; Replay создаёт новый job ID, сбрасывает Attempt и
сохраняет исходную audit record. Delete выполняется отдельно после разбора причины.
Неопределённая ошибка enqueue при replay может дать дубликат. После crash между
Save и Ack исходная доставка повторится, но final record останется доступной.

Shutdown сначала прекращает reserve и даёт handlers DrainTimeout на завершение,
затем отменяет их и ждёт остаток общего ShutdownTimeout. Повторный Shutdown ждёт
тот же done; Start запрещён до фактического завершения старой группы. Fatal одного
consumer отменяет всю группу. Произвольный Go handler принудительно не прерывается.

## Transactional outbox

SQL transaction приложения записывает бизнес-изменение и outbox row вместе.
Отдельный dispatcher публикует row с publisher confirmation, затем помечает delivered.
Crash между publish и delivered повторяет публикацию: consumer использует business ID
для дедупликации. Не держите DB transaction во время сетевой публикации и не называйте
два отдельных вызова SQL commit + enqueue атомарными.

Приёмка: [SQL claim/processes и SDK lifecycle](../../integration/queue_conformance_test.go),
[outage/quarantine](../../integration/queue_outage_test.go),
[DLX target outage](../../integration/rabbit_dlx_test.go),
[kill/restart и crash между Save/Ack](../../integration/worker_crash_test.go),
[dead-letter/drain regressions](../../queue/deadletter_test.go).
Эти тесты доказывают process/transport recovery, но не disk durability при потере host.

SDK contracts: [Redis Go client](https://redis.io/docs/latest/develop/clients/go/connect/),
[RabbitMQ publisher confirms](https://www.rabbitmq.com/tutorials/tutorial-seven-go).
[Quorum at-least-once DLX](https://www.rabbitmq.com/docs/4.3/quorum-queues) описывает
ограничения и требования к topology. Стенд задаёт internal publisher-confirm retry
timeout 1s для ускорения outage test; production выбирает его выше ожидаемого времени
confirm. Неподтверждённая повторная отправка может создать дубликат.
