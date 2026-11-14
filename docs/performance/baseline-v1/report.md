# Performance full: 12.3-v1

Source: `ac8306eec37796e90d5215db3a3d6fc62e912975`. Run: 2026-11-14T19:58:31.299Z → 2026-11-14T20:10:39.268Z.

CPU: Intel(R) Core(TM) i5-9300H CPU @ 2.40GHz; Go: go1.26.11; GOMAXPROCS 1/2; worker 2 CPU / 1 GiB.

Medians of five 500ms samples for a full run; MAD is median absolute deviation. Routing uses all routes round-robin. In-process timings exclude network; SQL timings include the actual driver/server.

| Workload | CPU | Standard ns/op | Chitu ns/op | Ratio | Chitu B/op | allocs/op | Chitu MAD |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Routing/Routes10 | 1 | 805.2 | 3847.0 | 4.78× | 2864 | 39 | 3.1% |
| Routing/Routes10 | 2 | 509.7 | 2710.0 | 5.32× | 2864 | 39 | 4.8% |
| Routing/Routes100 | 1 | 844.0 | 9895.0 | 11.72× | 8000 | 129 | 1.5% |
| Routing/Routes100 | 2 | 534.7 | 8529.0 | 15.95× | 8000 | 129 | 12.3% |
| Routing/Routes1000 | 1 | 941.4 | 77614.0 | 82.45× | 58499 | 1029 | 1.0% |
| Routing/Routes1000 | 2 | 575.9 | 59354.0 | 103.06× | 58502 | 1029 | 1.3% |
| DI/Transient | 1 | 84.5 | 1307.0 | 15.47× | 176 | 8 | 22.3% |
| DI/Transient | 2 | 48.8 | 602.9 | 12.35× | 176 | 8 | 5.9% |
| DI/Singleton | 1 | 3.2 | 87.8 | 27.36× | 8 | 1 | 1.4% |
| DI/Singleton | 2 | 2.0 | 81.3 | 39.81× | 8 | 1 | 13.0% |
| Rendering | 1 | 63011.0 | 62027.0 | 0.98× | 29081 | 317 | 3.0% |
| Rendering | 2 | 38977.0 | 40375.0 | 1.04× | 29087 | 317 | 1.6% |
| SQLBuild/postgresql | 1 | 83.4 | 3361.0 | 40.31× | 2384 | 42 | 6.0% |
| SQLBuild/postgresql | 2 | 56.4 | 2516.0 | 44.59× | 2384 | 42 | 8.1% |
| SQLBuild/mysql | 1 | 86.7 | 3102.0 | 35.76× | 2376 | 40 | 3.4% |
| SQLBuild/mysql | 2 | 57.5 | 2098.0 | 36.47× | 2376 | 40 | 1.1% |
| SQL/postgresql | 1 | 193846.0 | 217758.0 | 1.12× | 1472 | 29 | 5.5% |
| SQL/postgresql | 2 | 116580.0 | 131202.0 | 1.13× | 1472 | 29 | 5.7% |
| SQL/mysql | 1 | 355874.0 | 344523.0 | 0.97× | 1093 | 29 | 4.1% |
| SQL/mysql | 2 | 211298.0 | 219497.0 | 1.04× | 1093 | 29 | 7.0% |

| Soak | Healthy success | Recovery p99 ms | Fault 503 min | Heap after-before MiB | Recovery slope B/min | Goroutines before→after | Server connections before→after |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| postgresql/NetHTTP | 100.00% | 1.3 | 100.00% | 0.22 | -29373 | 3→3 | 1→1 |
| postgresql/Chitu | 100.00% | 1.3 | 100.00% | 0.07 | -147979 | 3→3 | 1→1 |
| mysql/NetHTTP | 100.00% | 1.6 | 100.00% | 0.05 | -12102 | 3→3 | 1→1 |
| mysql/Chitu | 100.00% | 1.7 | 98.65% | 0.08 | -16215 | 3→3 | 1→1 |

Server connection counts include the one observation connection, which is then closed. Histograms use 100µs upper-bound buckets. Soak memory includes client/server/generator and post-GC probes. This finite run establishes a baseline; operational RC observation remains separate.

Raw inputs: [core](core.txt), [SQL](sql.txt), [runtime](runtime.json), [manifest](manifest.json), individual soak JSON files, and [combined report](report.json).
