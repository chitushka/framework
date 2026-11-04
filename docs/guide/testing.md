# Качество и CI

## Уровни тестирования

- Unit tests проверяют чистые правила и граничные случаи.
- Package unit tests могут использовать контролируемые fakes; это не доказательство совместимости с сервисом.
- HTTP tests собирают router и выполняют запросы через `httptest`.
- Network fixtures в `cache/network_test.go` проверяют протокол на собственных TCP-серверах.
- Отдельный модуль `integration` проверяет реальные SQL/cache сервисы и AMQP infrastructure.

Тестируйте публичное поведение, отмену context, rollback и конкурентный доступ. Избегайте `time.Sleep`: используйте каналы, fake clock и deadlines.

## Локальные команды

```bash
make fmt-check
make vet
make test
make coverage
make lint
```

`make ci` объединяет все проверки. `lint` требует golangci-lint версии 2.13 или совместимой. Без локального Go тесты можно выполнить через Docker, как описано в корневом README.

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

Compose создаёт уникальный project/network без опубликованных портов, без bind mounts
и без внешних volumes. Credentials в YAML тестовые и действуют только в этой сети.
SQL data directories размещены в ограниченных tmpfs (PostgreSQL 256 MiB, MySQL 512 MiB),
поэтому этот стенд не проверяет disk durability и fsync при падении host.
PostgreSQL, MySQL, Redis, Memcached, RabbitMQ имеют health checks; runner стартует только
после их успеха. Tests имеют общий timeout 3 минуты, SQL/cache операции — contexts 30 секунд,
cleanup — 5 секунд, AMQP handshake/I/O — socket deadline. Job CI ограничен 15 минутами.

Runner выполняет `go test -mod=readonly -race -count=1 -timeout=3m -v ./...` внутри
`integration`. Это отдельный модуль с pinned драйверами и `replace => ..`; корневой
`go test ./...` не загружает эти зависимости и не обращается к сервисам. Отсутствие env
или сервиса вызывает **fail**, а не Skip. Локально и в CI выполняется один и тот же набор:

- PostgreSQL/MySQL: pool/ping, параметризованное чтение, commit и rollback savepoint,
  отменённый context, migrations up/repeated up/down, SQL cache, SQL queue enqueue/reserve/ack.
- Redis/Memcached/SQL cache: общий contract miss, binary payload, overwrite, delete и expiry TTL.
- RabbitMQ: SDK publish confirm/get/ack в exclusive queue; SDK adapter framework пока отсутствует.

Каждая таблица/ключ имеет случайное имя; cleanup использует независимый context, даже если
тест отменён. AMQP queue exclusive и удаляется при закрытии connection. Скрипт сохраняет
ненулевой test/build/startup exit code, при ошибке печатает service logs и всегда вызывает
`down --volumes --remove-orphans`. Ошибка cleanup тоже даёт ненулевой код.
При принудительном убийстве shell/host автоматический cleanup не гарантируется:
найдите project через `docker compose ls -a`, затем выполните
`docker compose -p PROJECT -f compose.integration.yml down --volumes --remove-orphans`.
Не используйте production credentials/DSN для стенда.

Это воспроизводимая база smoke/contract tests, не полный SQL conformance или crash/outage
suite. Следующие проверки перечислены в этапах 8–10; frontend/E2E/security gates — в этапе 12.

## Политика покрытия

`scripts/check-coverage.sh` строит профиль всех библиотечных пакетов и исключает `examples`: точки входа примеров тестируются в общем `go test ./...`, но не относятся к ядру фреймворка. Взвешенное statement coverage ядра не может быть ниже 80%.

Порог — страховочная сетка, а не цель для каждого пакета. Новый код должен тестировать значимые ветви, ошибки и инварианты; бессодержательные тесты ради процента не принимаются.

## GitHub Actions

Workflow `.github/workflows/ci.yml` запускается для pull request и push в `main`/`master`.
Job `test` (Ubuntu 24.04 / Go 1.26.x) проверяет gofmt, `go vet`, race detector, все unit tests
и coverage gate. Job `windows` (Windows 2025 / Go 1.26.x) выполняет vet/unit tests.
Независимый `lint` запускает официальный golangci-lint action. `integration` запускает
обязательный Compose baseline. Coverage profile сохраняется как artifact. Все jobs обязательны
по контракту; настройка required checks в branch protection выполняется владельцем репозитория.

Изменение обязательной проверки выполняется вместе с обновлением этого руководства, чтобы локальный и CI-контракты не расходились.
