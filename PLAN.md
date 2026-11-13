# План разработки Chitu Framework

Статусы: `[ ]` — запланировано, `[~]` — в работе, `[x]` — реализовано.

## Правила выполнения плана

- Каждый логически завершённый шаг после прохождения тестов и обновления документации фиксируется отдельным git-коммитом.
- В сообщении коммита кратко указывается реализованная функциональность.
- Статус `[x]` устанавливается только для проверенного и зафиксированного результата.
- Коммиты сохраняются только локально: отправку в удалённый репозиторий выполняет владелец проекта.

## Этап 0. Основание проекта

- [x] Инициализировать Go-модуль и Docker-окружение для проверок.
- [x] Закрепить module path `github.com/chitushka/framework`.
- [x] Закрепить идиоматический Go как стиль реализации, а архитектуру и DX Yii2 — как основу проектирования.
- [x] Зафиксировать архитектурные границы и правила зависимостей.
- [x] Добавить базовые команды форматирования и тестирования.

## Этап 1. Ядро

- [x] DI-контейнер: конструкторы, singleton, привязка интерфейсов, автоматическая сборка структур.
- [x] Event Bus: подписки, отписки, приоритеты, последовательная и конкурентная публикация.
- [x] Жизненный цикл приложения и модулей с корректным откатом при ошибке запуска.
- [x] Конфигурация из файлов и переменных окружения: JSON-слои, `.env`, системное окружение, типизированные значения, defaults и required-поля.
- [x] Реестр компонентов приложения и behaviors/decorators: именованные singleton/transient-фабрики, зависимости, обнаружение циклов и типобезопасная цепочка декораторов.

## Этап 2. Web

- [x] HTTP-приложение, request/response и цепочка middleware: HTTP context, JSON, redirect, middleware, обработка ошибок, управляемый `http.Server` и graceful shutdown.
- [x] URL Manager: статические и параметризованные маршруты, имена, генерация URL, вложенные группы, общие middleware, host/domain-шаблоны и регулярные ограничения.
- [x] Контроллеры, actions, BeforeAction/AfterAction и DI аргументов.
- [x] HTML/JSON/XML/file/stream responses и обработка ошибок.
- [x] Views, layouts, widgets и безопасная передача состояния в Vue.

## Этап 3. Данные

- [x] Connection поверх `database/sql`, конфигурация пула и реестр диалектов.
- [x] Транзакции и вложенные транзакции через savepoints.
- [x] Query Builder с безопасными параметрами.
- [x] Миграции и CLI-команды.
- [x] Active Record, связи, optimistic locking и генератор моделей.

## Этап 4. Прикладные подсистемы

- [x] Валидация и сценарии массового присваивания.
- [x] Cache API и драйверы Memory/Redis/Memcached/Database.
- [x] Сессии, Identity/User, CSRF и security middleware.
- [x] RBAC с иерархией ролей и разрешений.
- [x] Queue API и воркеры.
- [x] Интернационализация.

## Этап 5. Инструменты и поставка

- [x] CLI и генераторы migration/model/CRUD.
- [x] Vue 3 + Vite + AdminKit 3.5 и AssetBundle.
- [x] Структурированные логи, метрики, трассировка и debug toolbar.
- [x] Basic, Advanced Admin и REST API примеры.
- [x] Definitive Guide, CI, lint и контроль покрытия ядра не ниже 80%.

## Продолжение: от функционального каркаса к production-ready v1.0

Этапы 0–5 выше отражают наличие первоначальной реализации, а не сертификацию production-ready. Их история сохраняется. Новые шаги выполняются последовательно; `[x]` ставится после реализации, проверок и обновления документации. Каждый нумерованный шаг — самостоятельный инкремент; большие шаги при начале работы разбиваются на отдельные коммиты без потери критериев приёмки.

### Основания плана — анализ репозитория 04.11.2026

- В `web/server.go` есть header/shutdown timeouts, но остальные timeouts по умолчанию нулевые; в `web/context.go` JSON decoder не ограничивает размер body. Требуется явная политика ресурсов и доверия reverse proxy.
- `db/migration/runner.go` читает history и применяет pending migrations без межпроцессной блокировки, хотя `docs/guide/data.md` уже обещает advisory lock. Это расхождение реализации и документации.
- `db/connection_test.go` использует fake SQL driver, `cache/network_test.go` — собственные TCP-серверы. Такие тесты полезны, но не подтверждают совместимость с реальными СУБД и cache-сервисами.
- `queue/redis.go` и `queue/rabbitmq.go` предоставляют абстракции `RedisStore`/`RabbitChannel`, но готовых клиентских адаптеров в репозитории нет. В `queue/worker.go` финальная ошибка подтверждается Ack; отдельного durable dead-letter storage нет, settlement использует контекст без отмены и собственного deadline.
- Sessions сохраняют снимок состояния запроса; требуется проверить конкурентные изменения одной сессии, отзыв старого ID, cookie key rotation и совместимость writer wrappers со streaming.
- `observability/trace.go` экспортирует spans синхронно и игнорирует ошибку exporter; готового OpenTelemetry adapter нет. Необходимы проверки ограниченности памяти и влияния exporter на latency.
- `examples/advanced-admin/main.go` принимает username без пароля; REST example содержит самописный JWT и fallback secret, Basic — фиксированное имя CSS. Это учебные примеры, пока не production starters.
- `.github/workflows/ci.yml` проверяет Go, race, lint и общий coverage, но не запускает реальные зависимости, frontend build, E2E и vulnerability scan. `frontend/package.json` содержит только dev/build/preview.
- Наличие общего покрытия 80% не гарантирует проверку критических security/error ветвей. Утверждения в Guide нужно сверять с кодом и воспроизводимыми сценариями.

### Этап 6. Границы поддержки и воспроизводимая база — P0

- [x] **6.1. Зафиксировать контракт production-ready.** Создать матрицу поддерживаемых Go/OS, СУБД и версий сервисов; определить обязательный baseline и экспериментальные adapters. Описать deployment за TLS reverse proxy, модель угроз, границы ответственности framework/application и правила SemVer/deprecation. Приёмка: каждому поддерживаемому backend соответствует обязательный CI job; неподдерживаемые возможности явно обозначены.
- [x] **6.2. Сверить публичные обещания с реализацией.** Инвентаризировать README, Guide, REQUIREMENTS и exported API; завести таблицу «заявлено / реализовано / проверено». Исправить утверждение про migration lock и остальные неподтверждённые гарантии, обозначить demo authentication. Приёмка: у каждой гарантии есть ссылка на тест или явное ограничение, инструкции воспроизводятся из чистого checkout.
- [x] **6.3. Создать интеграционный стенд.** Добавить Docker Compose и отдельный integration test suite с настоящими SQL drivers, СУБД из baseline, Redis, Memcached и RabbitMQ. Предусмотреть health checks, deadlines, изоляцию данных и очистку после тестов. Приёмка: одна документированная команда локально и в CI выполняет обязательный набор без silent skip; unit tests сохраняют быстрый автономный запуск.

### Этап 7. HTTP и безопасность по умолчанию — P0

- [x] **7.1. Ограничить ресурсы HTTP.** Настроить body/header/form/upload limits, возврат 413/415, multipart cleanup и понятную политику timeouts для обычных запросов и streaming. Приёмка: тесты oversized/chunked bodies, медленных клиентов, отмены и освобождения ресурсов; ограничения применяются до JSON/form parsing.
- [x] **7.2. Ввести границу доверия proxy.** Добавить allowlist trusted proxy CIDR, безопасное определение client IP/scheme/host, host allowlist, redirect policy и CORS с корректными preflight/Vary/credentials. Приёмка: поддельные Forwarded/X-Forwarded-* от недоверенного клиента не меняют security-решения; есть таблица проверенных proxy-сценариев.
- [x] **7.3. Проверить HTTP-совместимость.** Уточнить HEAD/OPTIONS/405/Allow, escaping и encoded paths, trailing slash, file Range/conditional responses, streaming и disconnect. Все writer wrappers должны сохранять нужные возможности `http.ResponseController`/Unwrap и Flusher/Hijacker там, где они доступны. Приёмка: сетевые тесты, а не только recorder, подтверждают streaming и корректную цепочку session/logging/debug wrappers.
- [x] **7.4. Укрепить sessions и CSRF.** Определить конкурентное обновление session (CAS/locking либо явный запрет конфликтующих записей), idle/absolute TTL, отзыв старых ID, logout/revocation и cookie key rotation с ограничениями размера. Проверить CSRF для форм, JSON и login/logout. Приёмка: тесты параллельных запросов и двух экземпляров приложения, replay старого ID, key rotation и ошибок backend; secure cookie policy документирована.
- [x] **7.5. Подготовить reusable authentication.** Добавить password hashing через поддерживаемую криптобиблиотеку, проверку/rehash, login throttling и безопасный контракт token verifier. Для JWT использовать поддерживаемую библиотеку с allowlist алгоритмов, issuer/audience/expiry и ротацией ключей; не превращать framework в обязательный identity provider. Приёмка: негативные тесты credentials/token validation и отсутствие встроенных production secrets.
- [x] **7.6. Провести security-аудит HTTP и tooling.** Проверить XSS, SQL identifiers/raw expressions, mass assignment, path traversal/symlinks, open redirect, раскрытие ошибок и debug data. Ввести production-профиль, отключающий debug и скрывающий secrets. Приёмка: regression tests на найденные дефекты и threat-model checklist с доказательствами закрытия.

### Этап 8. Целостность данных и миграций — P0

- [x] **8.1. Межпроцессная блокировка миграций и recovery.** Session advisory/GET_LOCK на pinned connection, acquisition/release budgets, discard при неопределённом unlock, durable dirty marker и явный resolve. PostgreSQL transactional/nontransactional DDL и MySQL implicit commits проверены unit и process/crash tests на baseline. Контракт и CLI — docs/guide/migrations.md; реализация и acceptance tests зафиксированы локальным коммитом.
- [x] **8.2. Проверить SQL-контракты на каждой поддерживаемой СУБД.** Покрыть placeholders/quoting, NULL/time/decimal, affected rows/IDs, pool exhaustion, cancellation, savepoints, commit/rollback failures и DDL. Приёмка: единый conformance suite проходит по baseline; различия dialect описаны без неподтверждённой универсальности. Проверено: integration/sql_conformance_test.go: обе baseline СУБД, race, включая pool/query cancellation и connection death. Контракт — docs/guide/sql-contract.md.
- [x] **8.3. Укрепить Active Record.** Проверить optimistic locking при конкурентной записи, composite keys, relations/eager loading, pagination и N+1; согласовать validation и transaction boundaries. Приёмка: интеграционные тесты конфликтов и связей, bounded query count и отсутствие частичных обновлений после ошибки. Проверено: integration/ar_conformance_test.go на PostgreSQL/MySQL под race, unit overflow/relations и docs/guide/active-record.md.
- [x] **8.4. Проверить генераторы на результате.** Компилировать и тестировать сгенерированные model/CRUD/migration проекты; добавить защиту от небезопасных имён и непреднамеренной перезаписи, dry-run/diff. Приёмка: генерация из схем baseline даёт рабочий проект в чистом временном каталоге. Проверено: Generated projects из PostgreSQL/MySQL компилируются и исполняют tests; generated up/down SQL реально выполняется; CLI dry-run/diff и no-overwrite проверены. Контракт docs/guide/generators.md.

### Этап 9. Cache, очереди и отказоустойчивость — P0/P1

- [x] **9.1. Довести cache adapters до эксплуатации.** Проверить настоящие Redis/Memcached, TLS/auth/deadlines, reconnect, TTL semantics, большие значения и ограничения памяти memory cache. Определить stampede protection и атомарные операции, необходимые session/limiter. Приёмка: conformance и outage tests подтверждают согласованное поведение без бесконечного ожидания и роста памяти. Проверено: Memory/value budgets, coalesced loads, TLS/auth/deadlines/reconnect на настоящих Redis/Memcached; Redis CAS rotation и shared limiter проверены под race. Контракт docs/guide/cache.md.
- [x] **9.2. Поставить реальные queue adapters.** Реализовать RedisStore и RabbitChannel на поддерживаемых клиентах с reconnect, publisher confirms, lease/reclaim и атомарными переходами. SQL broker проверить несколькими процессами. Приёмка: enqueue/reserve/ack/retry после обрыва соединения и смерти consumer проходят на настоящих сервисах; явно заявлена at-least-once семантика. Проверено: Готовые go-redis/amqp091 adapters, SQL CAS/process consumers, lease/reclaim, confirms, quorum at-least-once DLX, outage/reconnect и quarantine. Приёмка integration/queue_conformance_test.go, queue_outage_test.go, rabbit_dlx_test.go; полный integration/race suite прошёл.
- [x] **9.3. Укрепить worker и retries.** Добавить per-job timeout, recovery panic handler, ограниченный settlement timeout, backoff/jitter, durable dead-letter и replay, корректный drain. Проверить потерю lease и повторную доставку; описать idempotency и transactional outbox. Приёмка: kill/restart и poison-message тесты не теряют подтверждённо принятые задачи; финальная ошибка остаётся доступной оператору. Проверено: durable Save до Ack, storage failure/abandon, replay, drain/restart/fatal, callback panic и process kill между Save/Ack на Redis/RabbitMQ; docs/guide/queues.md.

### Этап 10. Наблюдаемость и жизненный цикл — P1

- [x] **10.1. Добавить стандартные observability adapters.** OpenTelemetry SDK/OTLP HTTP для HTTP/SQL/queue, parent-based sampling, bounded batching, Stats/Flush/Shutdown; legacy tracer также асинхронный. Privacy attributes и finite labels, Prometheus parser; SQL trace columns включаются явно с миграцией и совместимостью старых NULL rows. Приёмка: collector trace tree на PostgreSQL/MySQL с тремя brokers, retries и отдельным worker process; stalled exporter, overflow, TLS/auth, outage/recovery и cleanup budgets. [Отчёт 10.1](docs/stage-10-1-acceptance.md), [контракт/миграция](docs/guide/observability.md). Go обновлён до 1.26.11 после GO-2026-6859; Linux race, real-service suite, coverage 84,4%, lint новых изменений — 0 замечаний.
- [x] **10.2. Завершить lifecycle и health contracts.** Разделить liveness/readiness, ограничить dependency probes, обработать SIGTERM, boot rollback, drain HTTP/workers и завершение exporters. Проверить зависшие modules и общий shutdown budget. Приёмка: process-level тест завершает приложение в срок при активных запросах и задачах; readiness снимается до остановки приёма работы. Проверено: независимый bounded rollback/shutdown, quiesce/reverse drain/finalizers, global probe slots, четыре Linux SIGTERM cases с Redis/PostgreSQL/collector; полный integration/race suite, coverage 84,6%, lint новых изменений и integration — 0 замечаний. [Отчёт 10.2](docs/stage-10-2-acceptance.md), [контракт](docs/guide/lifecycle.md).

### Этап 11. Полноценные starters и DX — P1

- [x] **11.1. Превратить Basic в рабочий starter.** Добавить browser login/logout с CSRF и validation, подключить AssetBundle через Vite manifest, автоматическую frontend build и production config. Приёмка: новый checkout собирается по инструкции, браузерный E2E проходит login/dashboard/logout без ручной отправки JSON и фиксированных hash filenames. Проверено: Node automatic build, embedded views/manifest assets, bounded sessions/limiter, production PHC/hosts/Secure cookies; Chromium desktop/no-JS mobile/production HTTPS — 3/3. Windows tests/vet, Linux vet/race, Basic lint — 0, coverage 84,6%, npm audit — 0. Добавлен CI browser job с artifacts. [Отчёт 11.1](docs/stage-11-1-acceptance.md), [Basic guide](examples/basic/README.md).
- [x] **11.2. Завершить Advanced Admin.** SQL users/RBAC/catalog/audit на PostgreSQL/MySQL, отдельные migrations/идемпотентный seed с явными PHC, HTML CRUD пользователей/справочника/ролей и назначения. Авторизация и аудит атомарны с mutation; delegation ceiling, last-admin guard, auth epoch и optimistic locking проверены реальными SQL/race tests. Chromium: guest/editor/admin/delegated manager, UI/direct HTTP escalation и сохранение данных после restart — 4/4. [Приёмка и ограничения одного процесса](docs/stage-11-2-acceptance.md).
- [x] **11.3. Завершить REST starter.** Внешний Ed25519 JWT verifier без fallback/issuer endpoint, полный CRUD, единый JSON error contract включая server/proxy/router, bounded pagination/filter/sort allowlists, IP/subject quotas и embedded OpenAPI 3.1. Contract/concurrency tests и отдельный production process E2E проверяют auth, CRUD, ошибки, 429 и SIGTERM. Документированы retry/idempotency, migration demo, backward compatibility и process-local memory storage/quotas. Windows vet/tests/E2E, Linux vet/race/E2E, coverage 84,6%, REST lint — 0 замечаний; отдельный CI job с artifacts. [Приёмка 11.3](docs/stage-11-3-acceptance.md).
- [ ] **11.4. Доработать руководство и onboarding.** Добавить cookbook deployment/testing/security/migrations/queues, runnable snippets, generator quickstart и migration guide API. Приёмка: примеры и ссылки проверяются автоматически, инструкции выполняются в чистом окружении.

### Этап 12. Непрерывная проверка качества — начинается с этапа 6

- [ ] **12.1. Расширить CI.** Добавить baseline OS/Go matrix, integration services, frontend lint/test/build, starter E2E и generator compilation. Security scan: govulncheck, npm audit с политикой исключений и сроков, secret scanning. Закрепить actions по SHA и автоматизировать контролируемое обновление зависимостей. Приёмка: все обязательные jobs реально выполнены и их результаты доступны; release невозможен при провале gate.
- [ ] **12.2. Усилить тестирование критических границ.** Сохранить общий coverage ядра ≥80%, отдельно контролировать auth/session/security и новые error branches; добавить fuzz targets для router/binding/query/protocol/token parsing. Исправить fail-fast/проверку входов coverage script. Приёмка: gate отвергает заведомо недостаточный/пустой профиль, fuzz smoke выполняется в CI, длительный прогон — по расписанию, race tests покрывают конкурентные сценарии.
- [ ] **12.3. Установить performance baseline.** Измерять routing/DI/rendering/SQL overhead, allocs, latency и memory при конкуренции; сравнивать с эквивалентным net/http baseline. Зафиксировать hardware, dataset, workload и допустимые регрессии до приёмки. Приёмка: воспроизводимый benchmark report и soak/fault-injection прогон без утечек goroutines/connections и неограниченного роста памяти.

### Этап 13. Release candidate и v1.0 — P1

- [ ] **13.1. Подготовить поставку.** Добавить LICENSE/NOTICE с проверкой frontend assets, SECURITY.md/private disclosure, CONTRIBUTING, changelog и release policy. Подготовить минимальные non-root runtime images для starters, SBOM и provenance релизных артефактов; подтвердить воспроизводимость сборки. Приёмка: RC устанавливается по version tag в сторонний модуль и проходит smoke deployment.
- [ ] **13.2. Провести эксплуатационную приёмку RC.** Развернуть эталонное приложение с SQL/cache/queue за TLS proxy, проверить backup/restore, migration recovery, rolling update, key rotation и аварии зависимостей. Задать измеримые SLO и длительность наблюдения перед прогоном. Приёмка: опубликован отчёт с нагрузкой, latency/error rate, ресурсами и результатами recovery; блокирующие дефекты закрыты регрессионными тестами.
- [ ] **13.3. Заморозить API и выпустить v1.0.** Проверить все обязательные пункты, compatibility suite и upgrade guide; экспериментальные adapters явно вынести из стабильных гарантий. Приёмка: нет открытых P0-дефектов и необоснованных исключений security gates, RC принят, release checklist подписан владельцем. Публикация tag/artifacts выполняется отдельным разрешённым действием.

## Уточнение по повторному аудиту 08.11.2026

Дефекты, последствия, evidence и порядок исправлений: [отчёт аудита](docs/development-audit-2026-11-08.md).
Проходящие unit tests не закрывают пункты с конкурентным доступом и отказами сервисов.

- [x] **A1. Устранить локальные дефекты cache/queue, выявленные аудитом.** Повторно проверять актуальную запись перед expiry-delete; учитывать cancel до memory dequeue; не изменять Attempt исходной доставки при неуспешном Nack; преобразовать panic handler в ошибку задачи; ввести JobTimeout и независимый SettlementTimeout. Приёмка: регрессии конкурентной перезаписи, отменённого reserve, Redis/AMQP send/Ack failures, panic/retry, deadlines всех ветвей Ack/Nack; unit/vet/race и документация defaults. Пройдены Windows tests/vet, Linux race всех пакетов и 20 повторов cache/queue, coverage gate (84,5% ядра; queue 86,5%), frontend build и integration baseline пяти сервисов с process/crash migration tests. Полный lint: 85 прежних замечаний (57 errcheck, 28 staticcheck); новые файлы/участки без замечаний. Реализация, тесты и Guide фиксируются отдельным локальным коммитом; 8.1/9.3 целиком не закрыты.
- [x] **A2 / 9.2 — P0. Закрыть гонку SQL reserve с Nack.** Между SELECT envelope и UPDATE lease отложенная задача может быть захвачена со старым Attempt. Применить CAS выбранного состояния или атомарный claim; детерминированный interleaving должен сохранять delay и увеличивать Attempt ровно один раз. Приёмка на PostgreSQL/MySQL с независимыми подключениями. Закрыто: CAS выбранного Attempt/available_at и детерминированный interleaving на обеих baseline СУБД.
- [x] **A3 / 9.3 — P0. Сохранить финальные ошибки до Ack.** Durable dead-letter с ошибками записи/replay; не подтверждать доставку при отказе хранилища. Затем явные состояния worker, запрет restart во время stopping, повторный Shutdown ждёт общий done, fatal останавливает всю группу. Crash/idempotency и callback failures — отдельные acceptance tests. Закрыто: FileDeadLetters, Save/Ack crash tests, Abandon, общий done, graceful drain и остановка группы.
- [ ] **A4 / 12.1–12.2 — P1. Восстановить достоверный quality gate.** Устранить известный полный lint debt; проверить threshold/profile и ошибки subprocesses coverage script негативными тестами. Добавить frontend build/test и компиляцию generator output в CI. Выполнять вместе с ближайшими изменениями, не откладывать весь контроль до RC. Частично в 11.1: отдельный frontend build/test, Basic browser E2E, npm audit high gate и browser artifacts в CI; в 11.2–11.3 добавлены Admin module/SQL/browser и REST lint/contract/process E2E jobs с artifacts. Полный прежний lint debt и negative coverage-script checks остаются открыты.
- [x] **A5 / 10.1–10.2 — P1. Ограничить exporter и cleanup.** Memory cache budgets/expiry закрыты в 9.1. Bounded trace export, наблюдаемые ошибки/потери и flush/shutdown budgets закрыты в 10.1. Независимый deadline application rollback/shutdown при отменённом Boot, общий budget зависших modules/hooks, quiesce/drain и process SIGTERM приёмка закрыты в 10.2. [Доказательства и ограничения](docs/stage-10-2-acceptance.md).

Связанные A2/A3/A5, этапы 8–10 и шаги 11.1–11.3 закрыты. Приёмка и ограничения — [отчёт этапов 8–9](docs/stages-8-9-acceptance.md), [отчёт 10.1](docs/stage-10-1-acceptance.md), [отчёт 10.2](docs/stage-10-2-acceptance.md), [отчёт 11.1](docs/stage-11-1-acceptance.md), [отчёт 11.2](docs/stage-11-2-acceptance.md) и [отчёт 11.3](docs/stage-11-3-acceptance.md). Ближайшие работы — 11.4 и незакрытые quality gates 12.

## Порядок выполнения и критерий завершения

Основной порядок: 6 → 7 → 8 → 9 → 10 → 11 → 13; этап 12 расширяется вместе с каждой подсистемой и должен быть закрыт до RC. P0 блокирует production-пилот, P1 — стабильный v1.0. Реальные интеграции из 6.3 обязательны для приёмки этапов 8–10; starters принимаются после стабилизации соответствующих контрактов безопасности и данных.

Production-ready означает подтверждённые безопасные defaults, целостность данных, предсказуемое восстановление после отказов, ограниченность ресурсов, поддерживаемый API и воспроизводимую поставку. Процент покрытия и наличие пакета сами по себе не закрывают эти требования.

## Ближайший инкремент

**Этап 11.4: доработать руководство и onboarding.** REST 11.3 принят 13.11.2026: внешний JWT, CRUD, единые errors, pagination/allowlists, bounded quotas и OpenAPI; Windows и Linux contract/process E2E, Linux race, coverage 84,6%, REST lint — 0. [Результаты и ограничения 11.3](docs/stage-11-3-acceptance.md).

Advanced Admin 11.2 принят 13.11.2026: persistent SQL users/RBAC/catalog/audit, migrations/seed, HTML CRUD/assignments и server-side authorization; PostgreSQL/MySQL race suite и Chromium restart/escalation E2E. Quality gates A4/12 расширены отдельным module vet/tests/lint и SQL/browser job с artifacts; прежний lint debt и negative coverage-script checks остаются. [Результаты 11.2](docs/stage-11-2-acceptance.md).

Этапы 8.1–8.4 и 9.1–9.3 завершены 09.11.2026. Пройдены Windows vet/tests, Linux vet/race ядра и adapters, полный real-service conformance/crash suite пяти сервисов, coverage 84,3% и lint новых изменений (0 замечаний). [Отчёт приёмки](docs/stages-8-9-acceptance.md) содержит точную область доказательств, migration/lease/DLQ настройки и ограничения. Полный прежний lint debt остаётся в 12.1; disk/host durability и production-ready v1.0 не объявляются.

Шаг 7.6 завершён: явный web production wrapper отключает toolbar/pprof, заменяет custom Router error handler на generic responses и скрывает panic details/raw paths в framework observability; production JSON logger редактирует sensitive keys/groups и opaque objects. Исправлены раскрытие HTTPError 5xx messages/invalid statuses, external symlink чтение migration source и SFC script breakout через generator Route; asset event attrs/srcdoc отвергаются, method/status labels metrics ограничены. Добавлены FileFromRoot/DownloadFromRoot на os.Root с network Range и traversal/symlink regressions. SQL identifier/value boundaries и scenario mass assignment проверены без неподтверждённых обещаний для raw SQL/HTML или application policy. Threat-model checklist, порядок подключения, ограничения redaction/файлов и evidence links — `docs/guide/security-audit.md`. Пройдены Windows tests/vet, Linux race (включая symlink cases), integration baseline пяти сервисов и cleanup, lint/gosec новых изменений (0 замечаний), coverage gate (84.7%). Govulncheck: 0 уязвимостей используемых packages/symbols; прежнее module-only сообщение для неимпортируемого OpenPGP не подавлялось. Полный прежний lint debt остаётся для 12.1; аудит не является production-сертификацией всего приложения.

Шаг 7.5 завершён: Argon2id PHC с ограниченными costs/concurrency, password verification и CAS rehash; CredentialStore/PasswordAuthenticator и атомарный bounded account/client limiter; TokenVerifier/Bearer и JWTVerifier на golang-jwt/jwt/v5 с явными algorithms/issuer/audience/typ/expiry/iat/max lifetime и атомарной ротацией public keys. Приложение предоставляет user persistence, shared limiter и identity provider; Basic/Admin остаются demo до этапа 11. REST переведён на внешний Ed25519 issuer, самописный JWT, /v1/token и fallback/client secrets удалены. Контракт/миграция — `docs/guide/authentication.md`. После обнаружения standard-library vulnerabilities минимальный Go обновлён до 1.26.10, Docker images закреплены по версии/digest, root/integration dependency graphs синхронизированы. Пройдены Windows tests/vet, Linux race, integration baseline всех пяти сервисов, lint новых изменений (0 замечаний), coverage gate (84.5% ядра; 86.0% auth). Govulncheck v1.9.0 на Go 1.26.10: 0 уязвимостей используемых packages/symbols; module-only GO-2026-5932 касается неимпортируемого x/crypto/openpgp и не подавляется. Прежний lint debt остаётся для 12.1.

Шаг 7.4 завершён: обязательный AtomicBackend, атомарная rotation/CAS и 409 вместо stale commit; idle/absolute TTL; freeze после commit; bounded CookieStore key ring с конечным grace deadline; CSRF body/header transport и очистка token при login/logout. Контракт и breaking migration описаны в `docs/guide/sessions.md`. Проверки охватывают две HTTP application instances с общим MemoryBackend, конкурентные update/regenerate/logout, replay ID, TTL/key rotation/backend errors и CSRF форм/multipart/JSON/login/logout. Внешний AtomicBackend не поставляется: простые cache adapters отклоняются, реальные сетевые реализации остаются для 9.1. Stateless CookieStore явно исключён из индивидуального revocation/CAS. Пройдены unit/network tests, vet, Linux race и 20 повторных race-прогонов session/CSRF, линтер новых изменений (0 замечаний) и coverage gate (84.3%); прежний lint debt из 7.3 остаётся за рамками этого шага.

Шаг 7.3 завершён: контракт описан в `docs/guide/http-compatibility.md`; сетевые HTTP/1.1 и TLS HTTP/2 tests проверяют routing/file responses и цепочку session/observability/debug. Пройдены `go test ./...`, `go vet ./...`, Linux race tests и порог общего покрытия ядра (83.9% при минимуме 80%). Полный golangci-lint v2.13.0 выявляет 88 прежних замечаний errcheck/staticcheck за пределами изменений шага; устранение этого долга остаётся для этапа 12.1. При повторных прогонах исправлены синхронизация ожидания streaming log и тест подмены cookie, который мог не менять исходное значение.
