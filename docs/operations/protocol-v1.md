# Протокол эксплуатационной приёмки 13.2 — v1

Протокол фиксируется отдельным commit **до измерений**. Изменение условий
требует новой версии протокола и полного повторного прогона. Smoke проверяет
стенд, но не принимается как эксплуатационный результат.

## Объект и границы

Frozen `v0.1.0-rc.1`, source commit и SHA-256 из принятого manifest 13.1.
Эталонное приложение собирается с root/adapters из проверенного `source.tar`,
без подмены текущим checkout. Application harness хранится отдельно и имеет
собственный source commit/hash. Linux/amd64; PostgreSQL 17, Redis 7.4 cache,
RabbitMQ 4.3 quorum queue; nginx с TLS 1.2/1.3. Все service images закреплены
digest. SQL и broker имеют отдельные Docker volumes; app — non-root/read-only.
Публикуется только TLS port на localhost; CA проверяется клиентом, insecure TLS
запрещён. Backend transport внутри изолированной сети — plaintext.

POST /work: JWT → Redis read/write → PostgreSQL durable outbox → HTTP 202.
Dispatcher публикует задачи с confirms; worker идемпотентно отмечает SQL effect.
Delivery at-least-once, effect на один operation ID — один. SQL является source
of truth; cache не содержит уникальных данных. Application supervisor заново
запускает worker после fatal transport error с ограниченной задержкой.

Это проверка одной интегрированной конфигурации, а не сертификация всех backend
комбинаций. Starters с process-local sessions/data не получают multi-replica
гарантии от результатов эталонного приложения. Открытые hosted gates 12.1/A4
остаются условием публикации.

## Нагрузка и длительность

Full: **600 секунд**, fixed arrival **20 POST/s**, уникальный operation ID,
до 64 одновременных запросов, deadline 2 секунды. Warmup и проверка readiness
выполняются до наблюдения. Клиент сохраняет status, latency, schedule lag, phase,
ID и replica для каждого запроса. Нельзя пропускать запланированные запросы или
повторять только неудачные измерения. Нагрузка работает во время управляющих
Docker операций, которые выполняются асинхронно.

| Фаза | Секунды | Действие |
| --- | ---: | --- |
| baseline | 60 | Одна replica |
| rolling | 60 | Additive migration, вторая replica, drain первой, rollback первой |
| rotation | 60 | JWT old → overlap → new-only и TLS certificate reload |
| sql-outage | 60 | SIGKILL SQL, 15s downtime, restart с тем же volume |
| cache-outage | 60 | SIGKILL cache, 15s downtime, restart и cold cache |
| queue-outage | 90 | SIGKILL broker, 15s downtime, restart, outbox catch-up |
| app-crash | 60 | SIGKILL одной replica при работающей второй, restart |
| final | 150 | Стабильное наблюдение и проверка утечек |

Smoke сокращает фазы до 8 секунд, downtime до 2 секунд, arrival до 5/s;
допускается только для отладки. Full запрещён при dirty checkout.

## SLO и блокирующие условия

- Baseline/rolling/rotation/final: HTTP 202 ≥99,9%, p99 ≤250ms.
- Все фазы: каждый запрос завершается ≤2100ms; schedule lag p99 ≤100ms,
  max ≤1000ms; число samples соответствует заданной длительности × arrival.
- SQL/cache после restart: 10 подряд успешных probes за ≤30s; queue: все
  принятые на этот момент операции обработаны за ≤60s после restart.
- App crash: доступность восстанавливается за ≤10s. Ожидаемые ошибки внутри
  явно обозначенных fault phases публикуются отдельно и входят в общий error
  rate. Исключение fault phases не скрывает их из полного отчёта.
- После нагрузки за ≤60s обработаны **все** подтверждённые HTTP 202 IDs;
  SQL effect каждого ID ровно один, нет failed/dead-letter jobs.
- Каждая app replica ≤256MiB RSS, ≤128MiB Go heap, ≤128 goroutines,
  SQL pool ≤8; final goroutines не растут более чем на 16 относительно baseline.
  Container memory/CPU и Go/SQL counters снимаются не реже раза в 5s.
- Rolling: обе версии реально обслуживают запросы, старая версия работает после
  expand migration; SIGTERM drain ≤15s, exit=0. JWT overlap принимает оба ключа,
  new-only отвергает старый. TLS leaf fingerprint изменился при доверенном CA.
- Backup/restore: после quiesce custom-format pg_dump восстанавливается в
  **новую** БД; полные отсортированные SQL rows/count/hash совпадают, restored
  app проходит TLS request; RPO=0 на quiesced snapshot, RTO ≤60s.
- Migration recovery: SIGKILL nontransactional migration после DDL оставляет
  dirty marker; повторный Up блокируется; оператор проверяет schema, выполняет
  Resolve с exact checksum, после чего Up идемпотентен. Recovery ≤60s.
- Ошибка сценария, OOM, отсутствие samples/evidence, cleanup failure или
  нарушение SLO делает весь full run неуспешным. Неуспешные runs сохраняются.

CPU limits: app 1 CPU/256MiB каждая, SQL 1 CPU/512MiB, cache
0,5 CPU/128MiB, broker 1 CPU/512MiB, proxy 0,5 CPU/128MiB.
Отчёт сохраняет host/Docker metadata, frozen RC identity, protocol/hash,
service/runtime digests, raw requests/resources/events, backup checksum и
validation results. Секреты, CA private key и bearer tokens не публикуются.
Host/power/disk loss, HA failover, WAN, длительные production soaks и
multi-node disaster recovery этим десятиминутным стендом не подтверждаются.
