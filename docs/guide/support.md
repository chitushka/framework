# Контракт поддержки и готовности к production

Проект находится до v1.0. Наличие API и прохождение baseline не означают production-ready:
этапы 7–13 в [плане](../../PLAN.md) содержат незакрытые security, durability и release gates.
Этот контракт задаёт проверяемый минимум совместимости. Область каждого обещания
ограничена тестами в [реестре гарантий](guarantees.md).

## Матрица baseline

| Компонент | Обязательная конфигурация | CI job | Область проверки |
| --- | --- | --- | --- |
| Go / Linux amd64 | Go 1.26.10+, ветка 1.26, Ubuntu 24.04 runner | `test`, `lint` | Unit, vet, race, coverage, lint |
| Go / Windows amd64 | Go 1.26.10+, ветка 1.26, Windows Server 2025 runner | `windows` | Unit и vet; без race |
| PostgreSQL | 17, Linux, pgx/v5 через database/sql | `integration` | Connection, параметры, DML/savepoint, последовательные migrations, SQL cache и queue smoke |
| MySQL | 8.4 LTS, Linux, go-sql-driver/mysql | `integration` | Тот же набор; DDL не считается транзакционным |
| Redis cache | 7.4, RESP2, TCP, password, DB 1 | `integration` | Binary set/get, overwrite/delete, TTL, AUTH/SELECT |
| Memcached cache | 1.6, text protocol, TCP | `integration` | Binary set/get, overwrite/delete, TTL |
| RabbitMQ infrastructure | 4.3, AMQP 0-9-1 | `integration` | SDK publish confirm/get/ack; **не** framework queue adapter |

Точные образы закреплены digest в [Compose](../../compose.integration.yml), версии Go-драйверов —
в [integration/go.mod](../../integration/go.mod) и go.sum. Minor-ветки выбраны как baseline,
а не как обещание совместимости со всеми версиями. Обновление digest/SDK выполняется PR
с полным прогоном стенда. Матрица описывает конфигурации проекта, а не срок поддержки поставщика:
[PostgreSQL](https://www.postgresql.org/support/versioning/),
[MySQL LTS](https://dev.mysql.com/doc/refman/8.4/en/mysql-releases.html),
[Redis](https://github.com/redis/redis/security),
[RabbitMQ](https://www.rabbitmq.com/release-information).

Обязательны все jobs workflow: `test`, `windows`, `lint`, `integration`. Администратор
репозитория должен включить их как required checks в branch protection: YAML сам по себе
не запрещает merge или release. CI запускает весь baseline без continue-on-error и silent skip.
Результат GitHub Actions появляется только после push владельцем; локальный прогон не заменяет его.

## Экспериментальные и отсутствующие возможности

- Oracle и SQL Server: есть dialect/query generation и fake-driver tests, нет реального
  conformance suite. MariaDB не приравнивается к MySQL baseline. SQLite не поставляется.
- macOS, ARM64, другие Go/OS и версии сервисов пока вне baseline.
- SQL queue: smoke подтверждает enqueue/reserve/ack одним процессом; leases,
  crash recovery, несколько процессов и durability остаются экспериментальными.
- RedisStore/RabbitChannel — интерфейсы queue adapters. Готовых SDK adapters нет;
  наличие RabbitMQ в стенде не означает готовности RabbitMQBroker к эксплуатации.
- Redis/Memcached backends не предоставляют TLS, cluster/sentinel, pooling, CAS и
  distributed stampede protection. Memcached backend не предоставляет SASL.
- Полная SQL conformance, конкурентные migrations/AR и внешние multi-process session
  adapters, durable dead-letter, OpenTelemetry SDK adapter и browser E2E ещё не подтверждены.
  Reusable password/JWT primitives проверены; production authentication flows starters
  и пользовательские CredentialStore/общий AttemptLimiter требуют отдельной приёмки.
- Frontend scaffold и три examples являются демонстрациями; они не входят в контракт
  готовых production starters. Полный список ограничений — в реестре гарантий.

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
| Параллельные записи и повторная доставка | Savepoints, optimistic-lock API, обязательный session AtomicBackend; migration lock отсутствует | Общий атомарный session backend, обработка 409, один migration runner, идемпотентные jobs, transaction boundaries, outbox |
| Отказ SQL/cache/broker/exporter, потеря процесса | Context/deadline API; нет полного outage/durability доказательства | Backup/restore, finite budgets, monitoring, retries с пределом, recovery процедуры |
| Утечка secrets/PII, debug endpoint | Debug opt-in и SQL args скрыты по умолчанию | Redaction логов/payload, закрытые metrics/pprof, секреты вне кода, least privilege |

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
