# Приёмка 10.2: lifecycle и health

Дата: 12.11.2026. Область: одно поколение Application, отмена startup,
rollback, graceful/forced shutdown, HTTP/workers/exporter и management health.

## Реализовано

- Общий shutdown budget 15s по умолчанию; SetShutdownTimeout до Start.
  Rollback и Run cleanup получают независимый от отмены startup context.
- Readiness снимается до stop hooks. Optional Quiescer прекращает reserve,
  затем modules завершаются в обратном порядке; NewFinalizer связывает cleanup
  exporter/ресурсов с этим порядком. Все этапы делят один deadline.
- Конкурентные Shutdown ждут общий done, получают сохранённую ошибку и не
  повторяют cleanup. Внешний Shutdown завершает Run. Startup cancellation и
  переход в running сериализованы. Cleanup panic преобразуется в ошибку.
- SignalContext обрабатывает interrupt/SIGTERM; все три examples переведены на него.
- Отдельные liveness/readiness handlers: 200/503, GET/HEAD, no-store, generic body.
  Readiness имеет общий probe timeout 250ms и четыре global slots по умолчанию,
  включая callbacks, игнорирующие отмену. Dependencies не влияют на liveness.
- Management Server имеет отдельный ID/component; HTTP повторный Shutdown ждёт
  drain, по deadline Close отменяет request contexts.

## Доказательства

| Сценарий | Проверка |
| --- | --- |
| Отменённый Boot, независимый rollback, общий deadline зависших modules/hooks, concurrent Shutdown, порядок finalizers | [app/lifecycle_test.go](../app/lifecycle_test.go) |
| Health startup/running/stopped, dependency failure/privacy, panic/cancel/deadline, global bound при 100 probes | [web/health_test.go](../web/health_test.go) |
| Повторный HTTP shutdown, forced Close/request cancellation, два listeners | [web/server_lifecycle_test.go](../web/server_lifecycle_test.go) |
| SIGTERM с активным HTTP и Redis job, PostgreSQL readiness и настоящим OTLP collector | [integration/lifecycle_process_test.go](../integration/lifecycle_process_test.go) |

Process suite прошёл под Linux/race: graceful 1,23s, hung HTTP 3,11s,
hung worker 3,07s, hung exporter 3,08s. Общий cleanup budget child — 2s;
допуск на полный выход процесса с race runtime — 5s. Перед остановкой admission
проверены readiness=503, liveness=200 и ещё доступный traffic listener. Graceful case
доказывает Ack активной задачи, сохранность следующей незарезервированной задачи,
flush exporter и наличие завершённого HTTP span у collector.

Полный real-service suite PostgreSQL/MySQL/Redis/Memcached/RabbitMQ/collector
завершился с кодом 0 за 99,322s. Воспроизведение из checkout:
`sh scripts/integration.sh` или `./scripts/integration.ps1`. Compose ограничивает
Erlang scheduler threads и GOMAXPROCS collector для небольших CI/Docker VM.

Windows tests/vet ядра и adapters прошли. Linux vet/race ядра, 20 повторов
app/web/queue, общий coverage gate прошли: **84,6%** при пороге 80%, app — 90,1%.
Lint изменённых строк ядра и полный lint/vet integration — 0 замечаний.
Проверены форматирование, git diff --check и относительные Markdown links.

Полный прежний lint debt ядра остаётся: 86 замечаний (57 errcheck, 28 staticcheck,
1 unused). A4/12.1–12.2 остаются открыты; этот шаг не объявляет весь CI зелёным.

## Границы

Полный контракт, composition examples и миграция — [lifecycle](guide/lifecycle.md).
Go не уничтожает некооперативные goroutines: deadline ограничивает ожидание,
после его исчерпания требуется выход процесса. Незавершённый Boot отвечает за
свои частичные ресурсы; Register не должен их захватывать. Startup deadline задаёт
caller. Hijacked HTTP connections и бизнес-идемпотентность принадлежат приложению.
Пределы не обещают успешный Ack/flush при forced shutdown или host/disk durability.
