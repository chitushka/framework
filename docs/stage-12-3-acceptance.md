# Этап 12.3 — performance baseline

Дата: 14.11.2026. [Протокол v1](guide/performance.md) и бюджеты зафиксированы
коммитом **b84a9c3 до замеров**. Проверенный harness — **ac8306e**; приёмочный run
выполнен из чистого source tree до и после измерения.

## Данные и воспроизведение

[Полный report](performance/baseline-v1/report.md),
[raw/combined JSON](performance/baseline-v1/report.json),
[manifest](performance/baseline-v1/manifest.json),
[runtime](performance/baseline-v1/runtime.json),
[Go build flags](performance/baseline-v1/go-env.json) и
[soak log](performance/baseline-v1/soak.log) сохранены в репозитории.
Run: **19:58:31–20:10:39 UTC**, 14.11.2026; четыре soak заняли 480,76s.

Host: Intel i5-9300H, 4 cores / 8 logical CPUs; OS RAM 8454787072 bytes
(7,87 GiB), Windows 10.0.19045.
Docker 29.2.0 / WSL2 kernel 6.18.58.1; VM 8 logical CPUs / 3,76 GiB RAM.
Worker Linux/amd64: 2 CPU quota, 1 GiB, Go 1.26.11, CGO=1,
GOFLAGS=-p=2 -mod=readonly. SQL: PostgreSQL 17.11 и MySQL 8.4.13, pinned images,
по 1 CPU / 512 MiB; остальные параметры — в protocol/manifest.

```bash
make performance
node scripts/performance-report.mjs docs/performance/baseline-v1
node scripts/performance-report.mjs docs/performance/baseline-v1 docs/performance/baseline-v1/report.json
```

Первая команда создаёт свежий run со своим hardware profile. Последние две
воспроизводят report и проверяют совместимость/пороговый comparator на сохранённых
данных; сравнение baseline с самим собой не является измерением улучшения.

## Benchmarks

**40 case/CPU сочетаний, 200 samples**: count=5, benchtime=500ms, CPU=1/2.
Измерены routing 10/100/1000 routes, DI transient/singleton, escaped view+layout,
SQL builder PostgreSQL/MySQL и реальные SQL queries на обеих СУБД.
Предварительные tests проверили одинаковые результаты fixtures; setup и warm-up
не входят в timing. Ns/op для RunParallel — обратная aggregate throughput.

Выборка medians при GOMAXPROCS=2; полная таблица и B/op/allocs — в report:

| Workload | Standard ns/op | Chitu ns/op | Chitu allocs/op |
| --- | ---: | ---: | ---: |
| Routing / 10 routes | 509,7 | 2710 | 39 |
| Routing / 100 routes | 534,7 | 8529 | 129 |
| Routing / 1000 routes | 575,9 | 59354 | 1029 |
| DI transient | 48,83 | 602,9 | 8 |
| DI singleton | 2,043 | 81,33 | 1 |
| Rendering / layout | 38977 | 40375 | 317 |
| SQL build / PostgreSQL | 56,42 | 2516 | 42 |
| SQL build / MySQL | 57,53 | 2098 | 40 |
| SQL query / PostgreSQL | 116580 | 131202 | 29 |
| SQL query / MySQL | 211298 | 219497 | 29 |

Максимальный MAD core cases — **22,26%**, SQL — **7,51%**, при заранее выбранном
лимите 25%. Первый набор был отклонён: Routes100/NetHTTP/CPU1 MAD=33,6%.
[Причина](performance/rejected-noise-v1/rejection.json) и
[отклонённые raw samples](performance/rejected-noise-v1/core.txt) сохранены.
Тот run остановлен, его неполные soak не принимаются. Повторён весь run без
изменения source/protocol/thresholds; samples разных runs не смешивались.

## Soak и faults

Четыре последовательных 120s runs, **48000 scheduled requests**, 100 requests/s,
max 8 in-flight. Каждый прошёл warm-up, healthy, pool exhaustion, slow SQL и
recovery. Pool slots удерживались реально; SLEEP/pg_sleep выполнялись реальными
серверами с 40ms SQL budget. Client body/status/id/name проверялись.

| Database / handler | Healthy+recovery success | Recovery p99 | Fault 503 min | Peak process RSS | Heap after-before |
| --- | ---: | ---: | ---: | ---: | ---: |
| PostgreSQL / net/http | 100% | 1,3ms | 100% | 20,12 MiB | +0,22 MiB |
| PostgreSQL / Chitu | 100% | 1,3ms | 100% | 20,49 MiB | +0,07 MiB |
| MySQL / net/http | 100% | 1,6ms | 100% | 20,53 MiB | +0,05 MiB |
| MySQL / Chitu | 100% | 1,7ms | 98,65% | 20,94 MiB | +0,08 MiB |

В MySQL/Chitu slow phase bounded admission отклонил 27/2000 запросов (1,35%);
остальные 1973 получили 503. Fault budget требует ≥80% 503; healthy/recovery
drop budget ≤1% соблюдён с нулём drops. Неожиданных HTTP/transport errors — **0**.

Во всех runs: goroutines **3→3**; application pool open/in-use после закрытия
**0/0**; backend sessions **1→1**, включая observation connection, которое затем
также закрыто. Recovery heap slope во всех случаях отрицателен, post-GC heap
не превысил budget +16 MiB. Cleanup **3,67–4,96ms** при budget 5s.
Результаты включают 24 resource samples на run, RSS/cgroup memory и DB pool stats.

## Проверки и выявленные ограничения

- Windows core tests; Linux core vet/race, REST process E2E, integration compile/
  vet с performance tag; полный lint четырёх modules — **0 issues**.
- Отдельные SQL fault phases под race прошли на обеих СУБД. Проверка поймала
  ошибку измерителя: HTTP body мог завершиться до возврата SQL connection в pool.
  Добавлено bounded ожидание drain в прежнем budget 5s; границы не ослаблялись.
- Negative Go/Node tests отвергают утечки heap/goroutines/connections, слишком
  долгий drain/cleanup, отсутствующие/повреждённые/noisy benchmark data, dirty
  source, несовместимые profiles и превышение каждого regression budget.
- Coverage gate: core **84,94%**, auth **86,60%**, session **87,31%**, security
  **94,73%**. Docs/onboarding, aggregate policy tests и actionlint прошли.
- CI performance job обязателен в aggregate gate, full reports публикуются как
  artifacts конкретного runner. Hosted run/required-check настройки остаются
  частью открытого 12.1/A4 и требуют push владельца.

**Найденная стоимость Router:** линейный обход, snapshot slice и создание params
map для каждого кандидата видны в [реализации](../web/router.go). При 1000 routes
получено **58502 bytes/op (57,13 KiB/op)** и 1029 allocs/op,
вместо 224 bytes/op / 4 allocs/op у net/http. Это измеренный performance follow-up
для оптимизации dispatch; быстрый Router на больших таблицах этим baseline не
объявляется. Soak использует один маршрут, поэтому его p99 нельзя переносить на
1000-route workloads.

SQL network ratios включают VM/server noise и не изолируют только цену wrapper;
Renderer использует тот же html/template и показывает близкие значения. Первый
baseline устанавливает исходную точку; future regression budgets (+20% ns/op,
+10% B/op, +1 alloc/op) проверяются по каждому case при совместимой среде.
Две минуты на вариант и post-GC slope не доказывают отсутствие любых утечек;
production TLS/deployment/SLO, cache/queue/exporter soak и длительное наблюдение
RC остаются эксплуатационной приёмке 13.2.
