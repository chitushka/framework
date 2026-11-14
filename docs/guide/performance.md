# Performance baseline: протокол 12.3 / v1

Протокол фиксируется до первого benchmark run. Результаты и точное оборудование
появятся в отдельном отчёте после исполнения. Изменение workload/budgets требует
новой версии протокола и свежего baseline; результаты разных версий не сравниваются.
Числовой источник параметров — [protocol.json](../../internal/performance/protocol.json).

## Среда и повторяемость

- Go из go.mod, Linux/amd64, Docker; worker: 2 CPU, 1 GiB RAM, GOMAXPROCS=2,
  GOFLAGS=-p=2 -mod=readonly. PostgreSQL/MySQL используют pinned baseline images из Compose,
  отдельные private databases, по 1 CPU/512 MiB; данные в tmpfs, без host ports.
  MySQL ограничен buffer pool 64 MiB, log buffer 8 MiB, max connections 32;
  performance_schema отключена. Эти настройки входят в source/profile baseline.
- Приёмочный host: Intel Core i5-9300H, 4 cores / 8 logical CPUs. В каждом run
  записываются CPU model, host/Docker RAM, kernel/Go/engine versions, cgroup limits,
  source commit и clean/dirty state. Turbo/frequency не фиксируются; shared VM и
  прочие host processes могут создавать шум. Замеры выполняются без race detector.
- Go benchmarks: benchmem, benchtime=500ms, count=5, cpu=1,2; warm-up до измерения,
  одинаковые fixtures и контроль результата. Времена — медиана пяти samples;
  разброс — median absolute deviation (MAD). MAD >25% требует повторного run.
- In-process benchmarks исключают listener/network/client allocation. Реальный
  HTTP/SQL soak отдельно измеряет полный loopback HTTP/1.1 keep-alive путь.

## Dataset и сравнения

| Workload | Chitu | Эквивалент стандартной библиотеки |
| --- | --- | --- |
| Routing, 10/100/1000 routes | Router, GET /itemsN/{id}, JSON | net/http ServeMux с теми же routes/JSON, round-robin hit по всем routes |
| DI, transient и singleton | Container, config → repository → service | те же три типа и constructors, ручная сборка/готовый singleton |
| Rendering | cached Renderer, view + layout | cached html/template, два Execute с теми же data/escaping/content |
| SQL build, PostgreSQL/MySQL | QueryBuilder: SELECT id/name, WHERE id/name, LIMIT 20 | тот же статический SQL и отдельно два bound arguments |
| SQL query, PostgreSQL/MySQL | Connection.QueryRowContext + Scan | тот же sql.DB.QueryRowContext + Scan, без observer |

CPU=1/2 используют RunParallel с отдельным request/sink на worker. Подготовка
fixtures, регистрация routes, parsing templates, создание DI и таблицы исключены
из timing. HTTP body/status/content type, template escaping, object graph и SQL
результат проверяются до замера. Неполные/нечисловые benchmark reports отклоняются.
RunParallel ns/op — wall time / total operations, то есть обратная aggregate
throughput; latency отдельных HTTP-запросов измеряется soak percentiles.

SQL dataset: 256 строк (id 1..256, name 128 ASCII bytes), primary-key lookup,
циклический id, read-only нагрузка, pool max-open/max-idle=4. Таблица создаётся
отдельно для run и удаляется после него. Benchmark query заранее собран; стоимость
QueryBuilder измеряется отдельно от сетевой SQL latency.
Rendering dataset: 20 строк, escaped title `Catalog <&>` и имена с `<item &>`
плюс 128 ASCII bytes. Перед timing прогреваются JSON/template/DI caches и SQL
query paths на каждом из четырёх соединений.

## Soak и fault injection

Каждая пара PostgreSQL/MySQL × net/http/Chitu выполняется последовательно,
**120 секунд**, всего 4 runs / 8 минут. Для обоих handlers используется тот же
SQL pool/dataset, GET по циклическому id, JSON body, обычные HTTP error statuses.

1. Warm-up: 20s, healthy.
2. Healthy: 20s.
3. Pool exhaustion: 20s; четыре connections удерживаются отдельно, запросы
   обязаны завершаться в пределах SQL budget, затем connections освобождаются.
4. Slow SQL: 20s; pg_sleep/SLEEP(0.2), SQL context budget 40ms.
5. Recovery: 40s, healthy.

Offered load: 100 requests/s, max 8 in-flight, без неограниченной очереди.
Client timeout 750ms; response body ≤4 KiB, всегда прочитан/закрыт. SQL budget
40ms действует также на ожидание pool slot. Успешный ответ проверяет id/name.
Во время faults ожидается 503; другие ошибки считаются отдельно.

Samples каждые 5s: post-GC heap, runtime goroutines, DB pool open/in-use/waits.
На границах phases также делается sample. RSS процесса и cgroup memory.current
сохраняются как диагностика; общий worker ограничен cgroup memory limit 1 GiB.
GC probe влияет на latency и одинаков для обоих вариантов. Histogram имеет
фиксированные buckets 100µs до 2s; percentile обозначает верхнюю границу bucket.
Client/server/generator находятся в одном процессе: memory включает все три.

Приёмочные budgets, выбранные до run:

- Healthy/recovery: ≥99% успешных scheduled requests, admission drops ≤1%,
  p99 ≤250ms. Warm-up исключён из latency/SLO gate.
- Каждый fault: ≥80% ответов 503; неожиданные errors/statuses ≤1%.
- На каждом sample open connections ≤4; после drain in-use=0; после Close open=0.
  Drain ограничен тем же budget 5s: получение HTTP body не означает, что последний
  server handler уже освободил SQL connection. Отдельное observation connection
  измеряет реальные backend sessions до/после; после app cleanup число не растёт,
  затем observation connection также закрывается.
- Post-GC heap от конца warm-up до окончания recovery растёт ≤16 MiB.
  OLS slope последних шести recovery samples ≤1 MiB/min; минимум шесть samples.
- После закрытия server/client transport/pool и GC goroutines ≤pre-run+6,
  post-GC heap ≤pre-run+16 MiB. Cleanup budget 5s; failure тоже делает run failed.

Это ограниченная по времени приёмка ресурсов, а не доказательство отсутствия
любой утечки или production SLO. RC soak/реальное deployment наблюдение — этап 13.2.

## Регрессии относительно baseline

Baseline хранит raw samples и medians, ns/op, B/op, allocs/op, HTTP percentiles,
memory/pool samples и environment. Следующий candidate сравнивается при той же
версии протокола, CPU/OS/Go/cgroup profile и тех же workload parameters.
Несовместимая среда, missing cases или noisy samples дают ошибку сравнения.

Допустимая регрессия median ns/op: **20%**, B/op: **10%**, allocs/op: **+1**
относительно baseline; каждый case/CPU проверяется отдельно. Нулевой baseline B/op
остаётся нулевым. SQL/network timing также требует совпадения service versions.
Исходный run устанавливает baseline; он не измеряет изменение относительно
предыдущего релиза. Ratios Chitu/standard library публикуются для оценки overhead.

Hosted CI создаёт report своего hardware profile и проверяет operational budgets;
локальные абсолютные ns/op нельзя переносить на другой runner. При обновлении
baseline нужен review причин изменения и новый полный run с исходными artifacts.

## Команды

Требуются Node 22, Docker Engine с Linux containers и Compose. Go/SQL drivers
выполняются внутри pinned Go image. Из корня репозитория:

```bash
make performance-smoke
make performance
```

Без make: `node scripts/performance.mjs smoke` или `node scripts/performance.mjs full`.
Smoke использует 1x/count=1/cpu=2, phases в десять раз короче, slope gate остаётся
для full run. Он проверяет harness и faults, но не принимается как baseline.
Full run требует чистого закоммиченного дерева до и после исполнения. Среда и
workload автоматически записываются в уникальный `.cache/performance/run-*`.

`latest.json` указывает на последний успешный full run; `latest-smoke.json` — на
последний smoke. Неуспешный run не заменяет успешный pointer и возвращает ненулевой
код. Все owned containers/networks/volumes удаляются в finally; ошибка cleanup
тоже блокирует приёмку. При потере host найдите `chitu-perf-*` через compose ls и
удалите только соответствующий test project.

Генерация report из сохранённых исходных artifacts и optional comparison:

```bash
node scripts/performance-report.mjs PATH_TO_RUN
node scripts/performance-report.mjs PATH_TO_CANDIDATE PATH_TO_BASELINE/report.json
```

Report parser требует все cases/cpu/repetitions, конечные metrics, runtime profile
и четыре успешных soak reports. Negative tests проверяют empty/corrupt/partial/
noisy data, dirty source, несовместимый hardware и превышение каждого бюджета.
CI job performance выполняет full run и сохраняет artifacts своего профиля.
