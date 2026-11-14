# Качество и CI

## Уровни тестирования

- Unit tests проверяют чистые правила и граничные случаи.
- Package unit tests могут использовать контролируемые fakes; это не доказательство совместимости с сервисом.
- HTTP tests собирают router и выполняют запросы через `httptest`.
- Network fixtures в `cache/network_test.go` проверяют протокол на собственных TCP-серверах.
- Отдельный модуль `integration` проверяет реальные SQL/cache/queue сервисы и OTel collector.

Тестируйте публичное поведение, отмену context, rollback и конкурентный доступ. Избегайте `time.Sleep`: используйте каналы, fake clock и deadlines.

## Локальные команды

Onboarding и documentation gate: `go mod download`, затем
`go run ./tools/checkdocs -run`. [Testing recipe](cookbook/testing.md) описывает
local links/anchors/source fences, negative checks и isolated external module.

```bash
make fmt-check
make vet
make test
make coverage
make fuzz
make lint
```

`make ci` объединяет module vet/tests, docs, positive/negative coverage, полный lint,
fuzz smoke, frontend и security. Coverage wrapper требует Node 22, `lint` —
golangci-lint 2.13.0; browser/real-service suites
выполняются отдельно. [Полный контракт gate](quality-gates.md) содержит artifacts,
security exceptions/expiry и release/required-check ограничения.

`make docs` проверяет Markdown, `make docs-test` также исполняет guide examples
в свежем Go-модуле. Эти команды входят в отдельный CI docs job и Windows job.

## Обязательный real-service baseline

Из корня чистого checkout с Docker Engine (Linux containers) и Compose v2:

```bash
sh scripts/integration.sh
```

В PowerShell: `./scripts/integration.ps1`. Если execution policy запрещает локальные scripts,
используйте process-only запуск: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/integration.ps1`.
Системная policy не изменяется. `make integration` вызывает тот же shell script, что и CI.
Устанавливать SQL drivers, Go или сервисы на host не требуется; первый запуск загружает образы
и зависимости и требует сети. Ветки/ограничения описаны в [support](support.md).

Compose создаёт уникальный project/network без опубликованных портов и внешних volumes.
Read-only bind mount содержит collector config; ephemeral volume с ограниченной ротацией
хранит traces и удаляется при cleanup. Credentials в YAML тестовые и действуют только в этой сети.
SQL data directories размещены в ограниченных tmpfs (PostgreSQL 256 MiB, MySQL 512 MiB),
поэтому этот стенд не проверяет disk durability и fsync при падении host.
PostgreSQL, MySQL, Redis, Memcached, RabbitMQ имеют health checks; runner стартует только
после их успеха. Collector health отдельно ожидается тестом с deadline.
Tests имеют общий timeout 6 минут, SQL/cache операции — contexts 30 секунд,
cleanup — 5 секунд, AMQP handshake/I/O — socket deadline. Job CI ограничен 15 минутами.
Холодная compilation generated project имеет отдельный deadline 90 секунд;
после неё SQL phase получает новый 30-секундный context, subprocess pipes — WaitDelay 1s.
Runner задаёт GOMAXPROCS=2 и GOFLAGS=-p=2, ограничивая параллельную компиляцию
на небольшом Docker host; конкурентные/process acceptance cases остаются обязательными.

Runner выполняет `go test -mod=readonly -race -count=1 -timeout=6m -v ./...` внутри
`integration`. Это отдельный модуль с pinned драйверами и `replace => ..`; корневой
`go test ./...` не загружает эти зависимости и не обращается к сервисам. Отсутствие env
или сервиса вызывает **fail**, а не Skip. Локально и в CI выполняется один и тот же набор:

- PostgreSQL/MySQL: SQL/AR conformance, generated project compilation/tests, process/crash migrations, queue consumers; pool/ping, параметризованное чтение, commit и rollback savepoint,
  отменённый context, migrations up/repeated up/down, SQL cache, SQL queue enqueue/reserve/ack.
- Redis/Memcached/SQL cache: общий contract miss, binary payload, overwrite, delete и expiry TTL.
- Redis/RabbitMQ: готовые framework adapters, leases/reclaim, confirms, retry/quarantine, outage/reconnect и kill/restart между durable Save и Ack.
- Redis/Memcached: TLS relay к реальным сервисам, auth, большие binary values; Redis session CAS/rotation и shared limiter.
- OpenTelemetry Collector: HTTP/SQL/Redis/RabbitMQ/SQL queue trace tree с retries,
  отдельный worker process, privacy; официальная Prometheus-библиотека разбирает metrics.

Каждая таблица/ключ имеет случайное имя; cleanup использует независимый context, даже если
тест отменён. Durable AMQP queues удаляются вместе с изолированным стендом. Скрипт сохраняет
ненулевой test/build/startup exit code, при ошибке печатает service logs и всегда вызывает
`down --volumes --remove-orphans`. Ошибка cleanup тоже даёт ненулевой код.
При принудительном убийстве shell/host автоматический cleanup не гарантируется:
найдите project через `docker compose ls -a`, затем выполните
`docker compose -p PROJECT -f compose.integration.yml down --volumes --remove-orphans`.
Не используйте production credentials/DSN для стенда.

Это conformance/crash/outage suite этапов 8–10.1. Он не доказывает disk durability
при потере host, cluster compatibility или browser E2E. Lifecycle/health — 10.2,
остальные gates — этапы 11–13. [Observability contract](observability.md).

## Политика покрытия

`scripts/check-coverage.sh` строит профиль всех библиотечных пакетов и исключает `examples`: точки входа примеров тестируются в общем `go test ./...`, но не относятся к ядру фреймворка. Взвешенное statement coverage ядра не может быть ниже 80%.

Тот же профиль обязан отдельно пройти auth ≥85%, session ≥85% и security ≥90%.
Пропущенный/пустой пакет не принимается; пороги сравниваются до округления.
Negative gate tests входят в `make coverage-test`; contract/error regressions
дополняют statement coverage. [Подробности](quality-gates.md).

Пороги — страховочная сетка. Новый код должен тестировать значимые ветви, ошибки и инварианты; бессодержательные тесты ради процента не принимаются.

## GitHub Actions

Workflow `.github/workflows/ci.yml` запускается для pull request и push в `main`/`master`.
Job `test` — matrix Ubuntu 24.04/Windows 2025 × minimum Go 1.26.11/latest patch
1.26.x. Все application modules проходят vet/tests, Linux — race, integration —
compile/vet без runtime services. Windows включает REST process E2E и onboarding.
Coverage имеет отдельный job с negative wrapper tests и critical package gates.
Обязательный `fuzz` исполняет восемь targets по 3 секунды; отдельный scheduled
workflow запускает их по 5 минут еженедельно. `make fuzz` повторяет smoke локально.
Job `docs` проверяет ссылки/snippets, fresh module и generator output, сохраняет
JSON test report; `basic`, `advanced-admin`, `rest` имеют отдельные starter E2E jobs.
Независимый `lint` проверяет все четыре modules. `frontend` и `security` включают
lint/build/test/audit, govulncheck и secrets. `integration` запускает Compose baseline.
`quality-gate` отклоняет также skipped/cancelled jobs; SHA pins и Dependabot описаны
в [quality contract](quality-gates.md). Reports сохраняются как artifacts. Все jobs обязательны
по контракту; настройка required checks в branch protection выполняется владельцем репозитория.

Job `performance` исполняет [performance protocol](performance.md): пять benchmem
повторов с CPU=1/2 и четыре 120s HTTP/SQL soaks с pool exhaustion/slow queries.
`make performance-smoke` проверяет harness при разработке; `make performance`
записывает полный report из чистого source commit. Ns/op сравниваются только при
совместимом hardware/runtime profile; absolute local numbers не переносятся на CI.

Изменение обязательной проверки выполняется вместе с обновлением этого руководства, чтобы локальный и CI-контракты не расходились.

Для DLX target-outage теста Compose задаёт RabbitMQ dead_letter_worker_publisher_confirm_timeout=1000ms.
Это ускоряет внутренний retry; production выбирает budget отдельно. Cache TLS relay
требует client certificate (mTLS), отрицательный случай без сертификата отклоняется.
[Итоги этапов 8–9](../stages-8-9-acceptance.md).
