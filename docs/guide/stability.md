# Публичный контракт v1.0 и границы стабильности

Версия v1.0.0 подготовляется как **неопубликованный draft**. Снимок API закреплён
локально; hosted CI, required checks, подпись владельца и отдельное разрешение
публикации остаются обязательными условиями [release checklist](../release-checklist-v1.md).

## Что заморожено

[API baseline](../../release/api-v1.json) содержит compiled Go API **27 packages /
991 declarations/selectors**: root library и SDK adapters. Проверяются exported
functions/vars/constants с типами и значениями, aliases, generic constraints,
публичные struct fields в порядке объявления и tags, interface methods,
value/pointer/promoted method sets. Private fields не фиксируются как layout,
но фиксируется невозможность внешнего unkeyed struct literal при hidden fields.

```bash
go run ./tools/apifreeze
node release/compatibility.mjs
```

Точная проверка отклоняет и удаления/изменения, и непроверенные добавления.
`-write` — явное обновление baseline после review; checksum в
`release/freeze-v1.json` не позволяет одной заменой snapshot скрыть drift.
Это inventory, а не полный Go semantic compatibility checker. Поведение,
persisted formats и влияние reflection покрывают consumer/service tests и
runtime fingerprint; новая возможность требует review совместимости и notes.

| Область | Предлагаемый стабильный контракт | Ограничение |
| --- | --- | --- |
| Core/web/config/CLI | Go signatures, documented defaults/env/flags, строгие HTTP/error/escaping contracts | Raw SQL/HTML, сторонние middleware и application authorization остаются за приложением |
| PostgreSQL/MySQL | PostgreSQL 17/pgx, MySQL 8.4/mysql driver; query/transaction/migration/AR contracts | MySQL DDL implicit commits; другие backend versions не приравниваются к baseline |
| Cache/session/auth | Redis 7.4, Memcached 1.6; memory budgets; CAS/rotation/TTL/409; Argon2id/JWT contracts | Stateless cookies не дают individual revocation, memory state process-local |
| SDK Redis/RabbitMQ | go-redis reliable queue/CAS/limiter; RabbitMQ 4.3 quorum/confirm/DLX | At-least-once, возможны дубликаты; не exactly-once и не защита от disk/host loss |
| SDK OTel | SDK/OTLP HTTP, bounded sampling/export/flush/shutdown | Export best-effort; SQL spans на boundary Connection/Transaction |
| Persisted formats | Session v1, queue Message/FailedJob JSON, migration IDs/checksums/history, opt-in trace fields | Менять форматы только с явной migration и rollout; никаких автоматических schema changes от номера версии |
| Starters/frontend | Source scaffolds Basic/Admin/REST, Vite manifest и перечисленные browser/REST contracts | Process-local sessions/quotas/catalog из README; source layout, demo views и AdminKit DOM/CSS не объявляются Go API |

## Экспериментальное и вне стабильных гарантий

| Возможность | Статус |
| --- | --- |
| `db.Oracle`, `db.SQLServer` и соответствующие dialect/query paths | Experimental backend: сигнатуры перечислены для review, runtime conformance отсутствует; production поддержка не обещается |
| MariaDB/SQLite, macOS/ARM64, другие Go/OS/backend combinations | Вне baseline; MariaDB не приравнивается к MySQL |
| Redis Cluster/Sentinel, Memcached SASL/cross-key CAS, distributed stampede control | Не поставляются как принятые adapters |
| OTLP gRPC, baggage/tracestate, driver-level tracing | Вне стабильного OTLP HTTP contract |
| Multi-node HA/PITR/WAN/power-loss recovery | Не подтверждены single-host operational acceptance |
| `internal/*`, tools, test harnesses, generated private helpers | Не являются consumer API; runtime changes всё равно требуют проверки принятого RC |

Экспериментальный backend не становится стабильным из-за наличия exported
constant или зелёного unit test. SDK `adapters/redis`, `adapters/rabbitmq`,
`adapters/otel` принимаются только в перечисленных baseline configurations.

## Promotion принятого RC

Runtime code/external dependencies должны совпадать с принятым v0.1.0-rc.1.
Compatibility gate сравнивает source set и нормализованные LF bytes; разрешено
только изменение собственных module version pins RC1→v1.0.0 и их checksum rows.
Любая другая dependency/source замена блокирует перенос RC evidence. Go API
проверяется и для текущего дерева, и для exact archived RC. Consumer устанавливает
frozen version в новый module cache без checkout replaces; затем выполняет
публичные fixtures, `go mod verify` и сборку Admin. Stable draft дополнительно
проверяется установкой именно v1.0.0 из своего source archive.

Номер module/image version — metadata promotion, не новый operational workload.
Принятый [13.2](../stage-13-2-acceptance.md) остаётся доказательством исходного RC;
он не переименовывается и его raw artifacts не меняются. Изменение runtime
потребует нового RC и новой operational acceptance.

После выпуска действуют [SemVer/deprecation](support.md#semver-и-deprecation):
breaking API/config/persisted/CLI changes требуют major; planned removal — после
deprecation минимум два minor/шесть месяцев и следующего major. Добавление
interface method тоже breaking для внешних implementations. Не расширяйте
заявленную backend матрицу без обязательного service CI и migration notes.
