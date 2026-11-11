# Приёмка шага 10.1 — observability

Дата: 11.11.2026. Область: стандартный OpenTelemetry adapter и ограниченный export.
Общий lifecycle/readiness/shutdown budget остаётся шагом 10.2.

## Реализация

- Отдельный `adapters/otel`: SDK/OTLP HTTP 1.47.0, HTTP server, SQL call и queue
  producer/consumer spans, W3C traceparent и parent-based ratio sampling.
- Неблокирующая ограниченная SDK очередь, конечные export/flush/shutdown budgets,
  наблюдаемые ошибки/потери и отмена network drain при shutdown deadline.
- Явные endpoint/TLS/auth, запрет redirects и ограничение collector responses.
  Spans не содержат URL, SQL/args, job names/payload, headers или exception messages.
- Legacy tracer переведён на bounded async export с Stats/Flush/Shutdown.
  SQL observers сохраняют diagnostic chain; envelope совместим со старыми сообщениями.
  SQL tracing включается через TraceContext и две nullable columns; старые NULL rows
  читаются, metadata переживает Nack, enqueue без подготовленной схемы не теряет trace молча.
- Collector Contrib 0.147.0 закреплён digest и включён в обязательный integration job.
  SDK adapters теперь также проверяются Windows job.
- Go minimum и Docker images обновлены до 1.26.11: govulncheck выявил
  [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859) в прежнем net/http 1.26.10.
  Generator compilation fixture и support matrix синхронизированы.

## Выполненные проверки

| Проверка | Результат |
| --- | --- |
| Windows Go 1.26.11: vet/tests ядра и adapters | Пройдены |
| Linux Go 1.26.11: vet/race ядра и adapters | Пройдены |
| OTel race tests, 20 повторов | Пройдены |
| Coverage gate ядра | 84,4%, порог 80% |
| Lint diff ядра; полный lint adapters/integration | 0 замечаний |
| Полный прежний lint ядра | 86 замечаний: 57 errcheck, 28 staticcheck, 1 unused; остаётся A4/12.1 |
| Govulncheck v1.9.0: ядро и adapters | 0 уязвимостей используемых packages/symbols |
| Gofmt, relative Markdown links, git diff whitespace | Пройдены |
| Полный real-service integration/race suite | Пройден, 96,084s; все шесть SQL/broker trace cases и отдельный worker process |

Govulncheck отдельно сообщает module-only advisories (1 в root graph, 3 в adapters),
которые не затрагивают используемые packages/symbols. Эти сообщения не подавлялись.
Удалённые GitHub Actions/branch protection локальным прогоном не подтверждаются.

## Доказательства и воспроизведение

Стандартная команда из checkout: `sh scripts/integration.sh` либо
`./scripts/integration.ps1`; process-only PowerShell ExecutionPolicy Bypass описан
в [testing](guide/testing.md). Runner использует Go 1.26.11, race, GOFLAGS=-p=2 и
GOMAXPROCS=2. Compose удаляет свои containers/network/volumes после прогона.
Generated project compilation имеет независимый бюджет 90s и WaitDelay 1s;
SQL-фаза после холодной сборки получает новый context 30s.

[Integration acceptance](../integration/observability_test.go) проверяет реальный
collector, PostgreSQL/MySQL и Redis/RabbitMQ/SQL queues: HTTP → SQL → producer →
две consumer attempts → SQL, семь spans на trace. Отдельный worker process получает
тот же trace через Redis; IDs и parent tree читаются из collector JSON с hex IDs.
Официальный Prometheus parser проверяет counters/gauge/histogram endpoint.
Остальной SQL/AR/generator/cache/queue/migration process/crash baseline запускается
в том же suite без silent skips обязательных сервисов.

[SDK tests](../adapters/otel/otel_test.go) проверяют sampling=0/1 и remote decisions,
privacy, parent tree, TLS/auth и redirect policy. При blocked exporter 5000 запросов
не ждут network, очередь ограничена восемью spans плюс один batch; число goroutines
не растёт на каждый request. [Outage tests](../adapters/otel/outage_test.go) проверяют
503/recovery, ограниченный response body, deadline drain и уже отменённый Shutdown.
[Legacy limits](../observability/export_test.go) и
[cleanup](../observability/shutdown_test.go) проверяют 256 queued spans, ограничение
размера значений, ошибки, sampling и освобождение очереди после deadline.

## Ограничения

Telemetry best-effort: при overflow/outage возможны потери, при partial success
состояние части batch неопределённо. Exporters должны соблюдать context;
произвольный некорректный plugin нельзя принудительно остановить в Go.
Нет baggage/tracestate propagation, OTLP gRPC или driver-level SQL instrumentation.
Rows/Scan/prepared Stmt/raw DB calls и transaction lifecycle требуют отдельного
instrumentation. Производительность проверена функциональными latency/resource
regressions; длительный soak/SLO остаётся 12.3. Production-ready v1.0 не объявляется.

Конфигурация, ownership, defaults и breaking migration legacy tracer —
[observability contract](guide/observability.md).
