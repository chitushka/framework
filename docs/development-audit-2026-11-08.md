# Повторный аудит проекта — 08.11.2026

Исторический срез на 08.11.2026. Найденные cache/queue/data дефекты и этапы 8–9
закрыты последующими инкрементами: [приёмка 09.11.2026](stages-8-9-acceptance.md).
Observability/application закрыты в этапах 10.1–10.2; CI 12.1/A4 остаётся
открытым до hosted runs/required checks. Актуальный статус — в PLAN.md;
исторические формулировки «открыто» в таблице ниже сохранены как срез аудита.
Известные P0, tests и evidence для выпуска сведены в
[release freeze](../release/freeze-v1.json) и [checklist](release-checklist-v1.md).

Проверены текущий план, архитектура, CI, документация и критические пути cache,
queue, migrations, Active Record, lifecycle и observability. Это аудит исходников
и локальных проверок, а не приёмка production-ready. Исходные незакоммиченные
изменения в `console`, `db/migration` и `integration` сохранены отдельно от исправлений аудита.

## Найденные дефекты и недоработки

| Приоритет | Проблема и последствие | Состояние / доказательство |
| --- | --- | --- |
| P0 | Panic handler очереди завершает весь процесс вместо обработки ошибки задачи. | Исправлено: `PanicError`, обычный retry/final policy; [регрессия](../queue/worker_resilience_test.go). Panic в application hooks остаётся за рамками исправления. |
| P1 | Memory cache читает expired snapshot под RLock, затем без повторной проверки удаляет ключ под Lock. Параллельный Set теряет свежие данные. | Исправлено повторной проверкой актуальной записи; [детерминированный interleaving](../cache/memory_concurrency_test.go) для TTL=0 и нового TTL. |
| P1 | MemoryBroker.Reserve выдаёт готовое сообщение до проверки отменённого context. Отменённый consumer может забрать задачу. | Исправлена проверка под mutex до dequeue; [регрессии](../queue/retry_regression_test.go) для wait=0 и wait>0. |
| P1 | Redis/RabbitMQ Nack изменяет `delivery.message.Attempt` до успешной отправки. Повтор после сетевой ошибки пропускает номера попыток и раньше исчерпывает MaxAttempts. | Исправлено построением нового envelope из исходной доставки; [регрессии](../queue/retry_regression_test.go) для Redis Retry, AMQP Publish и Ack failures. |
| P1 | Handler не имеет per-job deadline, Ack/Nack получают WithoutCancel без собственного срока. Зависший backend удерживает consumer после shutdown. | Добавлены JobTimeout=1m и SettlementTimeout=5s; [регрессии](../queue/worker_resilience_test.go) охватывают timeout, success/final Ack, retry Nack и settlement после отмены. Контракт кооперативный: произвольный Go-код принудительно не прерывается. |
| P0 | [Worker](../queue/worker.go) удаляет задачу Ack после финальной ошибки; OnFailure вызывается после удаления и не является durable storage. | Открыто, 9.3: сохранять dead-letter до Ack, проверять ошибки хранения, предоставить replay и idempotency contract. |
| P0 | [SQL reserve](../queue/database.go) читает envelope через SELECT, а UPDATE проверяет только ID и состояние lease. Между ними другой consumer может выполнить reserve+Nack; старый UPDATE захватит уже отложенную задачу со старым Attempt. | Найдено анализом interleaving, исполняемой регрессии пока нет. Открыто, 9.2: CAS выбранной версии/attempt и available_at либо атомарный claim с чтением актуального envelope. Проверять и delay, и счётчик на PostgreSQL/MySQL. |
| P1 | [Worker.Shutdown](../queue/worker.go) сбрасывает started до завершения consumers. Повторный Shutdown уже возвращает nil, Start может создать второе поколение workers; fatal одного consumer не останавливает остальных. | Открыто, 9.3: явные running/stopping/stopped, общий done до завершения, запрет пересечения поколений и остановка группы при fatal. |
| P1 | [Application.Start](../app/application.go) использует отменённый startup context для rollback; Run снимает отмену при shutdown без общего deadline. | Открыто, 10.2: независимый ограниченный cleanup budget, rollback при отмене Boot и process-level shutdown tests. |
| P1 | [Memory cache](../cache/memory.go) не ограничивает объём, удаляет expired entries только при чтении того же ключа. TTL не освобождает забытые ключи. | Открыто, 9.1: лимит entries/bytes, eviction/expiry cleanup, измеримый memory/soak contract. Исправление гонки expiry не закрывает этот пункт. |
| P1 | [Trace exporter](../observability/trace.go) выполняется синхронно, ошибка игнорируется. Отказ collector влияет на HTTP latency, сбои экспорта не наблюдаемы. | Открыто, 10.1: bounded batching, deadline, dropped/export-failure metrics, flush в shutdown budget. |
| P1 | [Coverage script](../scripts/check-coverage.sh) не валидирует threshold, скрывает ошибку go list за pipeline без pipefail; [CI](../.github/workflows/ci.yml) не проверяет frontend/generator output/security scans. | Открыто, 12.1–12.2: fail-closed gate с негативными тестами, полный lint без накопленного долга, frontend build/test и компиляция generated fixtures. Наличие job не доказывает его успешность. |
| P2 | Frontend собирается, но `@adminkit/core/dist/js/app.js` даёт chunk 913,63 kB (269,98 kB gzip), Vite предупреждает о размере. | 11.1 / 12.3: измерить используемые компоненты и загрузку starter, затем убрать лишние plugins/imports или разделить bundle; не скрывать предупреждение увеличением лимита. |
| P0 | Шаг 8.1 частично реализован в исходной рабочей копии, но ещё не принят и не отражён в Guide. | Продолжить отдельным инкрементом: реальные process/crash tests, pinned connection/lock cleanup, transactional/nontransactional DDL, dirty recovery и инструкция оператору. Не считать готовым по unit tests. |

## Порядок дальнейшей работы

1. Завершить и принять 8.1 на PostgreSQL/MySQL, согласовать Guide и CLI. Сохранить
   исходные изменения отдельным коммитом после их проверки.
2. Исправить SQL claim из 9.2. Детерминированно остановить второй consumer между
   SELECT и UPDATE, выполнить первым reserve+Nack, затем проверить delay и Attempt.
   Повторить с независимыми подключениями на обеих baseline СУБД.
3. Разделить 9.3: сначала durable final failure/replay и lifecycle worker, затем
   leases/reclaim, backoff/jitter и kill/restart. Проверять отказ dead-letter storage
   и невозможность Ack до сохранения финальной ошибки.
4. Вести 12.1–12.2 параллельно изменениям: убрать lint debt, исправить coverage gate,
   добавить frontend и generator compilation; после этого расширять SQL/AR conformance 8.2–8.4.
5. Ограничить memory cache и подготовить реальные queue/cache adapters (9.1–9.2),
   затем закрыть exporter/lifecycle budgets (10.1–10.2).
6. Production starters, E2E и RC оставить за этапами 11–13. Удалённый CI и branch
   protection проверяются отдельно от локальных прогонов.

Первые исправления меняют defaults worker: обработка получает deadline 1 минуту.
Приложения с долгими задачами должны явно настроить JobTimeout и согласовать lease
broker. Полный контракт и оставшиеся ограничения — [Production / Queues](guide/production.md#queues).

## Проверки

Пройдены Windows `go test -count=1 ./...` и `go vet ./...`, Linux
`go test -mod=readonly -race -count=1 ./...`, повторные race tests cache/queue
с `-count=20`, coverage gate: 84,5% ядра при пороге 80%; queue — 86,5%.
`npm run build` завершился успешно с предупреждением о размере chunk.
Полный golangci-lint v2.13.0: 85 замечаний (57 errcheck, 28 staticcheck),
в новых файлах и изменённых участках замечаний нет. Полный lint gate остаётся красным.
Интеграционный стенд прошёл на PostgreSQL, MySQL, Redis, Memcached и RabbitMQ;
включены исходные process tests миграций: два процесса применяют SQL один раз,
lock timeout/освобождение после смерти процесса, dirty recovery после смерти во время DDL,
явный PostgreSQL nontransactional DDL. Это подтверждает эти сценарии, но не закрывает
оставшуюся документацию и ревью шага 8.1.
Это результаты всей текущей рабочей копии, включая исходные изменения миграций.
Остальные результаты фиксируются в [PLAN.md](../PLAN.md) вместе с инкрементом.
Новые тесты проверяют наблюдаемое сохранение данных, номера попыток, retry после panic
и конечное ожидание, без требования к реализации конкретного алгоритма.
