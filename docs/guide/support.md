# Контракт поддержки и готовности к production

v1.0.0 подготовляется как unpublished draft; [API freeze](stability.md) закреплён
локально. Production-ready не объявляется до hosted acceptance и подписи
[release checklist](../release-checklist-v1.md). Этапы безопасности/данных/эксплуатации
приняты в указанной области; оставшиеся release gates — в [плане](../../PLAN.md).
Этот контракт задаёт проверяемый минимум совместимости. Область каждого обещания
ограничена тестами в [реестре гарантий](guarantees.md).

## Матрица baseline

| Компонент | Обязательная конфигурация | CI job | Область проверки |
| --- | --- | --- | --- |
| Go / Linux amd64 | minimum 1.26.11 и latest patch 1.26.x, Ubuntu 24.04 runner | `test` matrix, `lint`, `coverage` | Unit, vet, race, coverage, lint |
| Go / Windows amd64 | minimum 1.26.11 и latest patch 1.26.x, Windows Server 2025 runner | `test` matrix | Unit/vet, REST E2E и isolated onboarding; без race |
| PostgreSQL | 17, Linux, pgx/v5 через database/sql | `integration` | SQL conformance, concurrent/crash migrations и AR, generator compilation, cache/queue process recovery |
| MySQL | 8.4 LTS, Linux, go-sql-driver/mysql | `integration` | Тот же набор; DDL не считается транзакционным |
| Redis cache | 7.4, RESP2, TCP, password, DB 1 | `integration` | Binary/TTL, AUTH, TLS relay/outage, shared CAS/limiter, queue lease/reclaim |
| Memcached cache | 1.6, text protocol, TCP | `integration` | Binary/TTL, TLS relay/outage/reconnect и большие values |
| RabbitMQ queue SDK | 4.3, AMQP 0-9-1 | `integration` | Framework adapter: persistent publish confirms, retry/DLX/quarantine, consumer kill/restart |
| OpenTelemetry | SDK/OTLP HTTP 1.47.0, Collector Contrib 0.147.0 | `test`, `integration` | HTTP/SQL/queue trace tree, retries, отдельный worker process, sampling, bounded export/flush/outage; Prometheus parser |

Точные образы закреплены digest в [Compose](../../compose.integration.yml), версии Go-драйверов —
в [integration/go.mod](../../integration/go.mod) и go.sum. Minor-ветки выбраны как baseline,
а не как обещание совместимости со всеми версиями. Обновление digest/SDK выполняется PR
с полным прогоном стенда. Матрица описывает конфигурации проекта, а не срок поддержки поставщика:
[PostgreSQL](https://www.postgresql.org/support/versioning/),
[MySQL LTS](https://dev.mysql.com/doc/refman/8.4/en/mysql-releases.html),
[Redis](https://github.com/redis/redis/security),
[RabbitMQ](https://www.rabbitmq.com/release-information).

Обязательны `test`, `lint`, `coverage`, `frontend`, `security`, `integration`, `docs`,
`rest`, `basic`, `advanced-admin`, `performance`, `compatibility`. Aggregate **Required quality gate** проверяет
их success, включая matrix children. Администратор должен включить его как required
check и ограничить bypass/release permissions: YAML сам по себе не запрещает ручной
merge/release. [Контракт gate](quality-gates.md) и [статус приёмки 12.1](../stage-12-1-acceptance.md).
Результат GitHub Actions появляется только после push владельцем; локальный прогон не заменяет его.

## Экспериментальные и отсутствующие возможности

- Oracle и SQL Server: есть dialect/query generation и fake-driver tests, нет реального
  conformance suite. MariaDB не приравнивается к MySQL baseline. SQLite не поставляется.
- macOS, ARM64, другие Go/OS и версии сервисов пока вне baseline.
- SQL/Redis/RabbitMQ queue adapters проверены на baseline, включая процессы и transport
  recovery. At-least-once допускает дубликаты; fsync/disk crash при потере host не проверен.
- Redis/Memcached поддерживают TLS с проверкой сертификата и finite I/O/cancellation.
  Memcached text protocol не имеет SASL или cross-key session CAS. Cluster/sentinel и
  distributed stampede control вне baseline; Redis SDK даёт CAS/rotation и shared limiter.
- Persistent dead-letter требует application-owned volume либо иной реализации Store;
  container layer и volatile MemoryBroker не считаются durable storage.
- OTLP gRPC, baggage/tracestate propagation и driver-level SQL spans вне baseline.
  Bounded lifecycle/health приняты в 10.2; browser/REST authentication flows — 11.1–11.3,
  с указанными process-local sessions/quotas и application authorization границами.
- Basic/Admin/REST приняты как source starters с ограничениями из README, включая
  process-local state. Frontend demo views не объявляются готовым application product.
  Точная стабильная/экспериментальная область — в [матрице API](stability.md).

## Развёртывание за TLS reverse proxy

Baseline deployment: один TLS reverse proxy, закрытая сеть до HTTP listener приложения,
SQL/cache/broker доступны только приложению. Это целевая схема эксплуатации, не готовый
production профиль. Proxy завершает TLS, перенаправляет HTTP на HTTPS, проверяет Host,
ограничивает размер headers/body, скорость и число соединений и задаёт deadlines.
Listener приложения не должен быть доступен в обход proxy. Server/Router предоставляют
конечные body/form/upload/header limits и timeout defaults; приложение должно согласовать
их с proxy и workload. Значения, правила streaming и пример конфигурации описаны в
[HTTP resource policy](web.md#ограничения-ресурсов-http). Проверенная цепочка writer wrappers,
streaming и файловые validators описаны в [HTTP compatibility](http-compatibility.md).

Proxy удаляет входные Forwarded/X-Forwarded-* и формирует собственные значения.
Framework предоставляет явные trusted-proxy CIDR, host/redirect allowlists и CORS policy;
без конфигурации forwarding headers игнорируются. Настройка и проверенные сценарии описаны
в [proxy trust contract](proxy.md). Используйте проверенные Context accessors вместо raw headers.
Внешний origin задавайте явной конфигурацией. Cookies явно устанавливайте Secure,
HttpOnly и подходящий SameSite. Не включайте debug toolbar или pprof на публичном listener.
Приложение задаёт CSRF/CORS, allowlists, аутентификацию и авторизацию для своих маршрутов.

## Модель угроз и ответственность

| Угроза / граница | Framework сейчас | Обязанность приложения/оператора |
| --- | --- | --- |
| Недоверенный HTTP-клиент: exhaustion, spoofed proxy/Host | Resource limits/timeouts, explicit proxy trust, host/redirect allowlists и CORS | Закрытый listener, очистка headers на proxy, точные CIDR/host/origin policies, согласованные бюджеты и проверка входа |
| Пользовательский ввод: SQL injection, mass assignment, XSS | Параметры Query Builder, scenario allowlist, html/template и vueState; raw SQL/HTML обходят защиту | Allowlist identifiers, validation, запрет недоверенного raw SQL/HTML |
| Кража credentials/cookie, CSRF, escalation | Sessions/CSRF/RBAC, bounded Argon2id и credentials/throttling, public-key JWT verifier | Identity persistence/provider, общий limiter, TLS, secrets, MFA, permission/revocation checks |
| Параллельные записи и повторная доставка | Real SQL/AR conformance, session Redis CAS, pinned migration session lock, lease fencing | Общий session namespace, обработка 409, lease/job budgets, идемпотентные jobs, transaction boundaries, outbox |
| Отказ SQL/cache/broker/exporter, потеря процесса | Baseline conformance/process/outage и single-host operational RC acceptance; power/disk-loss не подтверждён | Backup/restore, finite budgets, monitoring, retries с пределом, recovery процедуры |
| Утечка secrets/PII, debug endpoint | Production handler блокирует debug/pprof, скрывает framework errors/raw path/panic; production logger редактирует sensitive attrs/opaque objects | Внешний production wrapper, безопасные fixed log messages, redaction application payload/exporters, закрытые metrics, секреты вне кода, least privilege; [checklist](security-audit.md) |

Не покрываются: скомпрометированный host/proxy/dependency, вредоносный модуль приложения,
гарантия exactly-once, автоматическая compliance и защита произвольного raw SQL/HTML.
Безопасность бизнес-правил и инфраструктуры не делегируется framework.

## SemVer и deprecation

До v1.0 minor-релиз 0.x может менять API; такие изменения должны иметь release notes
и migration instructions. Patch-релиз сохраняет публичные сигнатуры и совместимое поведение,
за исключением документированного исправления уязвимости. После v1.0 breaking changes
требуют новой major-версии и соответствующего Go module path `/vN` для N ≥ 2.

Публичный контракт включает exported API, конфигурацию, CLI flags, форматы persisted
данных и документированные HTTP/queue contracts. Добавление обязательного env/поля
или смена семантики сериализации считается изменением совместимости. Experimental API
явно обозначается в документации; downstream должен закреплять версию.

Плановое удаление после v1.0: `Deprecated:` в GoDoc, замена и migration guide;
сохранение старого API до следующего major и минимум два minor-релиза/шесть месяцев
(что дольше). Security-исключение описывается в release notes с причиной и способом миграции.
Поддержка новой Go/OS/backend конфигурации добавляется вместе с обязательной CI-проверкой;
удаление baseline требует объявления deprecation. Номер версии сам по себе не является
сертификатом production readiness.
