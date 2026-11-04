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
- [ ] **7.2. Ввести границу доверия proxy.** Добавить allowlist trusted proxy CIDR, безопасное определение client IP/scheme/host, host allowlist, redirect policy и CORS с корректными preflight/Vary/credentials. Приёмка: поддельные Forwarded/X-Forwarded-* от недоверенного клиента не меняют security-решения; есть таблица проверенных proxy-сценариев.
- [ ] **7.3. Проверить HTTP-совместимость.** Уточнить HEAD/OPTIONS/405/Allow, escaping и encoded paths, trailing slash, file Range/conditional responses, streaming и disconnect. Все writer wrappers должны сохранять нужные возможности `http.ResponseController`/Unwrap и Flusher/Hijacker там, где они доступны. Приёмка: сетевые тесты, а не только recorder, подтверждают streaming и корректную цепочку session/logging/debug wrappers.
- [ ] **7.4. Укрепить sessions и CSRF.** Определить конкурентное обновление session (CAS/locking либо явный запрет конфликтующих записей), idle/absolute TTL, отзыв старых ID, logout/revocation и cookie key rotation с ограничениями размера. Проверить CSRF для форм, JSON и login/logout. Приёмка: тесты параллельных запросов и двух экземпляров приложения, replay старого ID, key rotation и ошибок backend; secure cookie policy документирована.
- [ ] **7.5. Подготовить reusable authentication.** Добавить password hashing через поддерживаемую криптобиблиотеку, проверку/rehash, login throttling и безопасный контракт token verifier. Для JWT использовать поддерживаемую библиотеку с allowlist алгоритмов, issuer/audience/expiry и ротацией ключей; не превращать framework в обязательный identity provider. Приёмка: негативные тесты credentials/token validation и отсутствие встроенных production secrets.
- [ ] **7.6. Провести security-аудит HTTP и tooling.** Проверить XSS, SQL identifiers/raw expressions, mass assignment, path traversal/symlinks, open redirect, раскрытие ошибок и debug data. Ввести production-профиль, отключающий debug и скрывающий secrets. Приёмка: regression tests на найденные дефекты и threat-model checklist с доказательствами закрытия.

### Этап 8. Целостность данных и миграций — P0

- [ ] **8.1. Реализовать межпроцессную блокировку миграций.** Выбрать механизм по dialect, удерживать lock на правильном соединении, добавить timeout/cancel и безопасное освобождение при ошибке/падении. Раздельно обработать transactional и non-transactional DDL, dirty state и recovery. Приёмка: два независимых runner/process не применяют одну миграцию дважды; аварийный сценарий воспроизводится на настоящих СУБД.
- [ ] **8.2. Проверить SQL-контракты на каждой поддерживаемой СУБД.** Покрыть placeholders/quoting, NULL/time/decimal, affected rows/IDs, pool exhaustion, cancellation, savepoints, commit/rollback failures и DDL. Приёмка: единый conformance suite проходит по baseline; различия dialect описаны без неподтверждённой универсальности.
- [ ] **8.3. Укрепить Active Record.** Проверить optimistic locking при конкурентной записи, composite keys, relations/eager loading, pagination и N+1; согласовать validation и transaction boundaries. Приёмка: интеграционные тесты конфликтов и связей, bounded query count и отсутствие частичных обновлений после ошибки.
- [ ] **8.4. Проверить генераторы на результате.** Компилировать и тестировать сгенерированные model/CRUD/migration проекты; добавить защиту от небезопасных имён и непреднамеренной перезаписи, dry-run/diff. Приёмка: генерация из схем baseline даёт рабочий проект в чистом временном каталоге.

### Этап 9. Cache, очереди и отказоустойчивость — P0/P1

- [ ] **9.1. Довести cache adapters до эксплуатации.** Проверить настоящие Redis/Memcached, TLS/auth/deadlines, reconnect, TTL semantics, большие значения и ограничения памяти memory cache. Определить stampede protection и атомарные операции, необходимые session/limiter. Приёмка: conformance и outage tests подтверждают согласованное поведение без бесконечного ожидания и роста памяти.
- [ ] **9.2. Поставить реальные queue adapters.** Реализовать RedisStore и RabbitChannel на поддерживаемых клиентах с reconnect, publisher confirms, lease/reclaim и атомарными переходами. SQL broker проверить несколькими процессами. Приёмка: enqueue/reserve/ack/retry после обрыва соединения и смерти consumer проходят на настоящих сервисах; явно заявлена at-least-once семантика.
- [ ] **9.3. Укрепить worker и retries.** Добавить per-job timeout, recovery panic handler, ограниченный settlement timeout, backoff/jitter, durable dead-letter и replay, корректный drain. Проверить потерю lease и повторную доставку; описать idempotency и transactional outbox. Приёмка: kill/restart и poison-message тесты не теряют подтверждённо принятые задачи; финальная ошибка остаётся доступной оператору.

### Этап 10. Наблюдаемость и жизненный цикл — P1

- [ ] **10.1. Добавить стандартные observability adapters.** Реализовать OpenTelemetry propagation/export для HTTP/SQL/queue, sampling, bounded batching и flush; проверить Prometheus-совместимый endpoint. Ограничить labels и redact credentials/PII. Приёмка: end-to-end trace через приложение и worker виден в collector; отказ collector не блокирует запросы и не вызывает неограниченный рост памяти.
- [ ] **10.2. Завершить lifecycle и health contracts.** Разделить liveness/readiness, ограничить dependency probes, обработать SIGTERM, boot rollback, drain HTTP/workers и завершение exporters. Проверить зависшие modules и общий shutdown budget. Приёмка: process-level тест завершает приложение в срок при активных запросах и задачах; readiness снимается до остановки приёма работы.

### Этап 11. Полноценные starters и DX — P1

- [ ] **11.1. Превратить Basic в рабочий starter.** Добавить browser login/logout с CSRF и validation, подключить AssetBundle через Vite manifest, автоматическую frontend build и production config. Приёмка: новый checkout собирается по инструкции, браузерный E2E проходит login/dashboard/logout без ручной отправки JSON и фиксированных hash filenames.
- [ ] **11.2. Завершить Advanced Admin.** Подключить persistent users/RBAC/catalog, migrations/seed, формы CRUD и экраны ролей/назначений, аудит изменений и server-side authorization. Приёмка: admin/editor/guest E2E, сохранение данных после restart и запрет escalation через UI и прямые HTTP-запросы.
- [ ] **11.3. Завершить REST starter.** Убрать fallback secret, подключить проверенный auth adapter, ввести единый error contract, pagination/filter allowlists, rate limits и OpenAPI. Приёмка: contract tests и E2E проверяют auth, CRUD и ошибки; документирована идемпотентность и обратная совместимость API.
- [ ] **11.4. Доработать руководство и onboarding.** Добавить cookbook deployment/testing/security/migrations/queues, runnable snippets, generator quickstart и migration guide API. Приёмка: примеры и ссылки проверяются автоматически, инструкции выполняются в чистом окружении.

### Этап 12. Непрерывная проверка качества — начинается с этапа 6

- [ ] **12.1. Расширить CI.** Добавить baseline OS/Go matrix, integration services, frontend lint/test/build, starter E2E и generator compilation. Security scan: govulncheck, npm audit с политикой исключений и сроков, secret scanning. Закрепить actions по SHA и автоматизировать контролируемое обновление зависимостей. Приёмка: все обязательные jobs реально выполнены и их результаты доступны; release невозможен при провале gate.
- [ ] **12.2. Усилить тестирование критических границ.** Сохранить общий coverage ядра ≥80%, отдельно контролировать auth/session/security и новые error branches; добавить fuzz targets для router/binding/query/protocol/token parsing. Исправить fail-fast/проверку входов coverage script. Приёмка: gate отвергает заведомо недостаточный/пустой профиль, fuzz smoke выполняется в CI, длительный прогон — по расписанию, race tests покрывают конкурентные сценарии.
- [ ] **12.3. Установить performance baseline.** Измерять routing/DI/rendering/SQL overhead, allocs, latency и memory при конкуренции; сравнивать с эквивалентным net/http baseline. Зафиксировать hardware, dataset, workload и допустимые регрессии до приёмки. Приёмка: воспроизводимый benchmark report и soak/fault-injection прогон без утечек goroutines/connections и неограниченного роста памяти.

### Этап 13. Release candidate и v1.0 — P1

- [ ] **13.1. Подготовить поставку.** Добавить LICENSE/NOTICE с проверкой frontend assets, SECURITY.md/private disclosure, CONTRIBUTING, changelog и release policy. Подготовить минимальные non-root runtime images для starters, SBOM и provenance релизных артефактов; подтвердить воспроизводимость сборки. Приёмка: RC устанавливается по version tag в сторонний модуль и проходит smoke deployment.
- [ ] **13.2. Провести эксплуатационную приёмку RC.** Развернуть эталонное приложение с SQL/cache/queue за TLS proxy, проверить backup/restore, migration recovery, rolling update, key rotation и аварии зависимостей. Задать измеримые SLO и длительность наблюдения перед прогоном. Приёмка: опубликован отчёт с нагрузкой, latency/error rate, ресурсами и результатами recovery; блокирующие дефекты закрыты регрессионными тестами.
- [ ] **13.3. Заморозить API и выпустить v1.0.** Проверить все обязательные пункты, compatibility suite и upgrade guide; экспериментальные adapters явно вынести из стабильных гарантий. Приёмка: нет открытых P0-дефектов и необоснованных исключений security gates, RC принят, release checklist подписан владельцем. Публикация tag/artifacts выполняется отдельным разрешённым действием.

## Порядок выполнения и критерий завершения

Основной порядок: 6 → 7 → 8 → 9 → 10 → 11 → 13; этап 12 расширяется вместе с каждой подсистемой и должен быть закрыт до RC. P0 блокирует production-пилот, P1 — стабильный v1.0. Реальные интеграции из 6.3 обязательны для приёмки этапов 8–10; starters принимаются после стабилизации соответствующих контрактов безопасности и данных.

Production-ready означает подтверждённые безопасные defaults, целостность данных, предсказуемое восстановление после отказов, ограниченность ресурсов, поддерживаемый API и воспроизводимую поставку. Процент покрытия и наличие пакета сами по себе не закрывают эти требования.

## Ближайший инкремент

**Шаг 6.1: зафиксировать контракт production-ready и матрицу поддержки.** Результат: документ с обязательным baseline, моделью deployment/угроз и критериями приёмки; ссылки на него из README/Guide. После этого выполнить 6.2 и сформировать integration matrix для 6.3. В рамках текущего изменения выполнены анализ и планирование; реализация новых шагов ещё не начата.
