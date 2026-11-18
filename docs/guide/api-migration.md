# API migration guide до v1.0

Для принятого RC1 отдельно описан [переход к v1.0.0](upgrade-v1.md).

Это переход от раннего функционального каркаса к принятым контрактам этапов 7–11,
а не обещание совместимости любого pre-v1 commit. Сначала закрепите исходный и
целевой commit/version, сохраните DB/storage backup и выполните собственные tests
на копии данных. Не смешивайте в одном rollout несовместимые session/queue schemas.

## Что изменить в приложении

| Прежнее использование | Текущее подключение и действие |
| --- | --- |
| Нулевые HTTP timeouts, неограниченный JSON/form | Конечные defaults; явно настройте RequestLimits и streaming write budgets. Обработайте 413/415. [HTTP contract](http-compatibility.md) |
| Raw Forwarded/X-Forwarded headers | Явные proxy CIDR/host allowlists, Context accessors; generic production responses через NewProductionHandler. [Proxy](proxy.md), [audit](security-audit.md) |
| Обычный cache adapter как session storage | AtomicBackend, CAS/rotation и 409 conflict. Старые envelopes/cookies требуют повторного входа; смените cookie name/namespace или завершите старые nodes. [Sessions](sessions.md#миграция-и-доказательства) |
| CSRF в query string или JSON field | Один body form field либо один header; после Login/Logout/Regenerate получите новый token GET-запросом. [CSRF](sessions.md#csrf-формы-json-loginlogout) |
| Username-only login, built-in REST issuer/HS256 fallback | PasswordAuthenticator/Argon2id + CredentialStore/AttemptLimiter; внешний Ed25519 issuer для REST. Нет /v1/token/JWT_SECRET. [Authentication](authentication.md#starters-миграция-и-доказательства) |
| Migration up без dirty recovery | Session lock, history checksums/state, явный resolve после проверки схемы. Сохраните source прежних migrations; upgrade history/schema из конкретного старого deployment сначала проверьте на копии. [Migrations](migrations.md) |
| Decimal model как float64 | Regenerate с decimal string и nullable pointers, отдельно мигрируйте JSON clients; preview не разрешает overwrite. [Generators](generators.md), [quickstart](generator-quickstart.md) |
| Финальная ошибка job → Ack | Durable DeadLetterStore в WorkerConfig, независимые settlement/drain budgets; storage failure не даёт Ack. [Queues](queues.md) |
| Старые classic AMQP queues | Новые имена quorum main/delay/invalid с принятым DLX contract; существующий queue type не меняется redeclare. Спланируйте остановку consumers и перенос сообщений. [Adapters](queues.md#готовые-adapters) |
| SQL queue без trace columns | Старый broker остаётся untraced; для TraceContext opt-in добавьте nullable columns и используйте согласованный rollout. [Observability](observability.md) |
| Синхронный legacy SpanExporter | Bounded async export; в tests вызывайте Flush, после drain — Shutdown, проверяйте Stats/ErrExport. Нет implicit tracing без HTTPConfig.Tracer. [Legacy tracer](observability.md#legacy-tracer-изменение-совместимости-до-v10) |
| Неограниченный cleanup и signal только Interrupt | Application finite shutdown budget, quiesce/reverse drain/finalizers, app.SignalContext с SIGTERM; lease/grace/exporter budgets согласуйте. [Lifecycle](lifecycle.md#ограничения-и-миграция) |
| Application error handler только в Router | Для ранних server limit errors используйте WithServerHandlerWrapper; JSON-only REST wrapper не предназначен для streaming/upgrades. [HTTP wrappers](http-compatibility.md) |

## Starters

Basic теперь использует HTML login/logout с CSRF и Vite manifest; прежнюю ручную
отправку JSON замените browser flow, production требует PHC/host config.
[Basic migration](../../examples/basic/README.md).

Advanced Admin имеет отдельный Go module, SQL migrations и явный seed.
Прежние `/admin/rbac` и `/admin/dictionaries` заменены формами roles/assignments/catalog.
Пользователи/RBAC/catalog/audit persistent, sessions/limiter process-local.
[Admin migration](../../examples/advanced-admin/README.md).

REST сохраняет success `data`, добавляет `meta` и `error.code`, по умолчанию
возвращает только первые 20 записей. Переведите list clients на пагинацию,
POST/PUT — на input только с title; messages теперь generic. Каждый token audience
разрешает весь CRUD общего in-memory каталога. POST неидемпотентен,
Idempotency-Key не поддерживается. [REST contract](../../examples/rest-api/README.md).

## Порядок обновления

1. Сверьте используемые API с таблицей и обновите код, config, generated clients.
2. Проверьте schema/storage/key formats на отдельной копии, подготовьте совместимый
   expand/contract rollout либо окно остановки; решите вопрос повторного login.
3. Выполните docs/snippet tests, unit/vet/race и затронутые real-service/browser
   suites по [testing recipe](cookbook/testing.md). Тестируйте failure/retry paths.
4. Примените migrations отдельным job, переключите traffic только после readiness,
   проверьте error rate и export/worker failures. Backup restore, SLO и решение
   о rollback проверяются для вашего deployment отдельно.

Новые nullable fields и error codes в REST требуют tolerant clients. Breaking
изменения принятого REST `/v1` требуют `/v2` и отдельной migration guide. Общая
SemVer/deprecation policy и границы pre-v1 — в [support contract](support.md).
