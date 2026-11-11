# Tracing, bounded export и Prometheus

Шаг 10.1 поставляет `github.com/chitushka/framework/adapters/otel` в отдельном Go-модуле.
Ядро не импортирует OpenTelemetry SDK. Adapter закрепляет SDK/OTLP HTTP v1.47.0,
реализует `observability.HTTPTracer`, `db.QueryObserver` и `queue.Observer`.
Глобальные OTel provider, propagator и ErrorHandler не изменяются.

## Подключение

Приложение устанавливает framework и adapters совместимых версий. При разработке
из checkout используйте replacements, как в `integration/go.mod`.

```go
import (
    "context"
    "log"
    "os"
    "time"

    telemetry "github.com/chitushka/framework/adapters/otel"
    "github.com/chitushka/framework/observability"
    "github.com/chitushka/framework/queue"
)

provider, err := telemetry.NewHTTP(context.Background(), telemetry.Config{
    ServiceName: "orders",
    SampleRatio: 0.1,
}, telemetry.HTTPConfig{
    Endpoint: os.Getenv("TRACE_ENDPOINT"), // https://collector.example/v1/traces
    Headers: map[string]string{"Authorization": os.Getenv("TRACE_AUTHORIZATION")},
})
if err != nil {
    log.Fatal(err)
}

metrics, err := observability.NewMetrics()
if err != nil {
    log.Fatal(err)
}
handler := observability.HTTP(observability.HTTPConfig{
    Tracer: provider, Metrics: metrics,
})(router)

jobs, err := queue.New(broker, provider)
if err != nil {
    log.Fatal(err)
}
worker, err := queue.NewWorker(broker, queue.WorkerConfig{Observer: provider})
if err != nil {
    log.Fatal(err)
}
// Зарегистрируйте handlers, запустите HTTP server/worker и используйте jobs.Push(ctx, ...).
// После HTTP/worker drain завершите export независимым от request/Boot context:
cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := provider.Shutdown(cleanup); err != nil {
    log.Print(err)
}
```

`router` и `broker` принадлежат приложению; `handler` передаётся HTTP server,
а `metrics` — отдельному закрытому `/metrics` endpoint. Включайте production wrapper
в порядке из [security audit](security-audit.md). Общая автоматическая оркестрация
readiness/drain/exporter shutdown относится к шагу 10.2; здесь ownership явный.

`NewHTTP` требует полный URL с `/v1/traces`, без userinfo/query/fragment. TLS ≥1.2,
проверка server certificate обязательна; `TLSConfig` позволяет задать CA/mTLS.
Plain HTTP требует явного `Insecure: true` и предназначен для изолированного стенда.
Auth headers копируются из явной конфигурации (не более 16, значения ≤4096 bytes).
Redirects не выполняются, retries отключены, response headers ограничены 32 KiB,
body collector — 64 KiB. Endpoint/auth не выбираются из OTel environment variables.
Приложение отвечает за хранение credentials и доступ к collector.

Для другого транспорта используйте `New(config, sdktrace.SpanExporter)`.
Ownership exporter переходит provider; exporter обязан соблюдать context и завершать
свои goroutines/соединения в Shutdown. Произвольный некорректный plugin нельзя
принудительно остановить средствами Go; goroutine на каждый span не создаётся.

## Propagation и sampling

HTTP middleware извлекает W3C `traceparent`, создаёт server span и передаёт его в
response header. `Inject(ctx, headers)` используется для исходящих HTTP-запросов;
сам HTTP client span автоматически не создаётся. Невалидный parent игнорируется.
Inject добавляет traceparent в переданный carrier; приложение отвечает за headers,
которые уже находились в нём, и не должно копировать входные headers целиком.
`SpanFromContext` даёт общие trace/span IDs для framework logging/debug.

Queue producer записывает `traceparent` в необязательное поле JSON envelope;
SQL/Redis/RabbitMQ сохраняют его при retry, lease reclaim и dead-letter replay.
Worker извлекает parent из сообщения, создаёт consumer span на каждую попытку,
включая handler и settlement; ошибки/panic/timeout отмечаются generic error status.
Parent каждой повторной доставки — исходный producer; at-least-once допускает несколько
consumer spans. Сообщения прежнего формата работают и начинают новую worker trace.
Размеры traceparent/tracestate envelope ограничены 512 bytes на поле.

Для SQL broker включите `DatabaseConfig.TraceContext: true`: SchemaSQL добавит
nullable `traceparent`/`tracestate VARCHAR(512)` columns. Существующие таблицы
сначала мигрируются явно (для baseline PostgreSQL/MySQL):

```sql
ALTER TABLE jobs ADD COLUMN traceparent VARCHAR(512) NULL;
ALTER TABLE jobs ADD COLUMN tracestate VARCHAR(512) NULL;
```

Обновите consumers и включите TraceContext до подключения producer observer.
NULL metadata старых строк начинает новую worker trace. Без TraceContext прежняя
схема работает; попытка отправить traced message возвращает ошибку до INSERT,
предотвращая молчаливую потерю propagation. Автоматического ALTER TABLE нет.

Для privacy adapter передаёт только traceparent: baggage и tracestate не копируются.
Последнее поле оставлено в envelope для совместимых внешних интеграций.
Контекст запуска worker не становится родителем всех его задач.

Sampling — SDK ParentBased + TraceIDRatioBased. `SampleRatio` должен быть конечным
числом [0,1]; ноль отключает новые root traces. Parent sampled=0/1 сохраняется,
в том числе remote parent. Если клиентам нельзя задавать sampling, приложение/proxy
должно снимать входной traceparent на своей границе доверия. Даже sampled=1 не снимает
resource budgets экспортера.

## SQL и границы измерения

HTTP/worker contexts автоматически получают SQL observer. Для CLI/background кода
используйте `provider.WithSQL(ctx)`. Повторный WithSQL того же provider не добавляет
дублирующий observer; `db.AddQueryObserver` и toolbar сохраняют предыдущий observer.

Spans охватывают вызовы `Connection`/`Transaction` exec/query/prepare и
Connection.QueryRowContext. Начало восстанавливается по измеренной duration;
это не driver-level instrumentation. Чтение rows, Scan errors, готовые sql.Stmt,
прямой `DB()`/pinned connection, Begin/Commit/Rollback не получают автоматических spans.
QueryRow создаёт событие до Scan, поэтому поздняя Scan error в нём неизвестна.
SQL-текст, args, connection DSN и error messages не экспортируются.

## Ограничения ресурсов и ошибки

| Настройка | Default | Допустимый диапазон |
| --- | --- | --- |
| QueueSize | 1024 spans | 1–8192 |
| BatchSize | min(128, QueueSize) | 1–min(512, QueueSize) |
| BatchInterval | 200 ms | 1 ms–1 min |
| ExportTimeout | 1 s | 1 ms–30 s |
| FlushTimeout | 5 s | 1 ms–30 s |

SDK BatchSpanProcessor использует неблокирующую очередь: при заполнении теряется
телеметрия. Одновременно удерживаются ограниченные queue/batch buffers; transport
не запускается на каждый запрос. Attributes ограничены 16 на span и 128 символами
значения. ServiceName ≤128 bytes, только доверенное значение конфигурации.
Имена spans фиксированы: HTTP method из 9 стандартных/OTHER, SQL operation из
фиксированного списка, `queue publish`/`queue process`; status ограничен 100–599,
attempt — 0–1000. Raw URL, query strings, user/job IDs/names, payload, SQL,
headers, exception messages и stack traces не записываются.

`Stats()` возвращает Ended (sampled), Exported, Failed, ExportErrors и Unexported.
Последний счётчик включает ожидающие spans и потери заполненной SDK queue; после
Flush без активных запросов это число потерянных spans. Это не gauge текущей длины.
Failed считает spans из неуспешных/неопределённых batches; при partial success
часть из них могла быть принята collector. Exactly-once учёт доставки не обещается.
Счётчики накопительные; нет хранения неограниченной истории ошибок или spans.
SDK ErrorHandler получает generic ошибку без исходного текста exporter.

`Flush(ctx)` ограничен меньшим из caller deadline и FlushTimeout; он экспортирует
уже законченные spans, но не ждёт активные handlers. `Shutdown(ctx)` прекращает SDK
recording, дренирует очередь и закрывает transport. При исчерпании shutdown budget
cancelled lifetime прерывает текущий export и дальнейший network drain.
Вызовы Flush/Shutdown сериализованы с ограниченным ожиданием. Ошибки/потери
сообщаются через `observability.ErrExport`; прежние ошибки за жизнь provider
сохраняют этот результат даже после восстановления collector. Stats позволяет
различить последующие успешные exports. Deadline возвращается как context error.

## Legacy tracer: изменение совместимости до v1.0

`observability.NewTracer(SpanExporter...)` теперь тоже экспортирует асинхронно:
256 ожидающих spans, один worker, ≤16 exporters, 1 s на ExportSpan,
Flush/Shutdown ≤5 s. Проверки экспортированных данных после HTTP response
должны вызвать Flush. Приложение обязано вызвать Shutdown после drain.
Без явного HTTPConfig.Tracer middleware больше не создаёт implicit spans/traceparent.
Stats содержит Accepted/Completed/Dropped/Failures/Pending. Переполнение и ошибки
больше не скрываются; Flush/Shutdown после потерь возвращают ErrExport.
При deadline Shutdown оставшаяся очередь освобождается. Exporter должен соблюдать
context. Remote sampling bit сохраняется. Legacy tracer обслуживает только HTTP;
для SQL/queue используйте SDK adapter. Его raw paths/panic messages вне production
остаются development diagnostics; production wrapper по-прежнему их скрывает.

## Prometheus и воспроизводимая приёмка

`Metrics.ServeHTTP` выдаёт text exposition 0.0.4: counters requests/errors,
active gauge и cumulative duration histogram, включая +Inf/sum/count.
Labels — только allowlist method и status 100–599; route/URL/пользователи не labels.
Parsing endpoint официальной библиотекой Prometheus проверяется в integration.

Запуск: `sh scripts/integration.sh` или `./scripts/integration.ps1`.
Compose добавляет закреплённый Collector Contrib 0.147.0 с memory limiter и
ограниченной ротацией JSON file exporter в удаляемом volume; ports наружу закрыты.
Read-only bind mount содержит только collector config. Tests ждут collector health
с deadline; отсутствие endpoint/file/configuration приводит к fail.

Доказательства: [SDK propagation/sampling/redaction/latency](../../adapters/otel/otel_test.go),
[outage/recovery/shutdown](../../adapters/otel/outage_test.go),
[legacy limits](../../observability/export_test.go),
[legacy cleanup](../../observability/shutdown_test.go),
[реальный collector, SQL, три brokers, retries и отдельный worker process](../../integration/observability_test.go).
Это функциональная fault/trace приёмка, не длительный performance baseline/SLO этапа 12.3.

Контракты upstream: [SDK batching/flush](https://pkg.go.dev/go.opentelemetry.io/otel/sdk/trace),
[OTLP HTTP exporter](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp),
[Collector configuration](https://opentelemetry.io/docs/collector/configuration/).
