# 13.2 — эксплуатационная приёмка frozen RC

Дата: **18.11.2026**. Шаг **13.2 принят локально** для конфигурации ниже.
Публикация, API freeze и production-ready v1.0 этим отчётом не объявляются.

## Объект и воспроизводимость

Framework **v0.1.0-rc.1**, frozen source
`90b2c3bf936c5d7f67eb2a4b9216d438b1f40739`, archive SHA-256
`95d79daedbda472a6ebdf8a0a2a5e8b984b5ba9888d1fc5ed1b8cb416f3386d8`.
Идентичность совпадает с [поставкой 13.1](stage-13-1-acceptance.md).
В Docker build накладывалось только эталонное приложение; framework/adapters
брались из проверенного архива, без замены текущим checkout.

[Протокол v1](operations/protocol-v1.md) зафиксирован **до измерений** commit
`0415a7b88f2e97a3cfd2a9a46ea98609c716d8bb`. Workload, SLO и budgets после
измерений не менялись. Проверенный harness — commit
`d746704b21a4d260d8e995b22e8c9668e478fa3e`, cleanBefore/cleanAfter=true;
app/harness SHA-256 и runtime/service digests — в
[manifest](operations/baseline-v1/manifest.json).

Full run: 14:14:05–14:25:24 UTC, собственно наблюдение **600,271s**,
**12 000 POST**, fixed arrival **20/s**, 150 resource samples.
Windows host: i5-9300H, 8 logical CPUs, Node 22.22.3; Linux/amd64 Docker 29.2.0,
8 CPUs, 4 038 758 400 bytes RAM. App replicas: 1 CPU/256MiB каждая;
PostgreSQL 17: 1 CPU/512MiB; Redis 7.4: 0,5 CPU/128MiB;
RabbitMQ 4.3: 1 CPU/512MiB; nginx: 0,5 CPU/128MiB. Images закреплены digest.
App — UID/GID 65532, read-only, dropped capabilities, no-new-privileges.

Единственный host port — localhost TLS; проверялись CA/hostname, без insecure TLS.
Proxy заменяет forwarded headers и имеет узкую доверенную /32 в приложении.
SQL/cache/AMQP transport внутри изолированной сети — plaintext.

## Измерения и SLO

| Фаза | Запросы | Ошибки | Успешность | p99, ms |
| --- | ---: | ---: | ---: | ---: |
| Baseline, 60s | 1200 | 0 | 100% | 132,54 |
| Rolling, 60s | 1200 | 1 | 99,917% | 168,47 |
| JWT/TLS rotation, 60s | 1200 | 1 | 99,917% | 156,20 |
| SQL outage, 60s | 1200 | 793 | 33,917% | 757,72 |
| Cold cache outage, 60s | 1200 | 443 | 63,083% | 306,67 |
| Queue outage, 90s | 1800 | 0 | 100% | 159,78 |
| App crash, 60s | 1200 | 1 | 99,917% | 448,44 |
| Final stable, 150s | 3000 | 0 | 100% | 131,96 |

Штатные baseline/rolling/rotation/final удовлетворяют **≥99,9% и p99 ≤250ms**.
Общий error rate **10,325%**, p99 **753,18ms**: намеренные отказы зависимостей
включены в эти числа. Это не обещание 99,9% доступности при SQL/cache outage.
Все request deadlines и schedule lag gates прошли; lag p99 по фазам ≤15,03ms.

Максимумы по Go samples: RSS **17,83MiB**, heap **3,41MiB**, goroutines **37**,
SQL pool **6** при лимите 8. Проверка роста goroutines baseline→final прошла.
CPU/container memory samples для всех ролей сохранены в raw resources и
агрегированы в [JSON report](operations/baseline-v1/report.json).
OOM и final failed jobs не наблюдались.

## Recovery и целостность

- **Rolling:** additive migration, новая application revision 2 пишет новую
  column, revision 1 продолжает работать. Обе реально обслужили нагрузку;
  старая replica удалена из upstream перед SIGTERM, drain **2,217s**, exit=0,
  затем возвращена после expand migration. Это rollout двух конфигураций
  reference app на одном frozen framework image, а не upgrade framework API.
- **JWT/TLS:** overlap принимает old/new kid; new-only отвергает old.
  Nginx reload заменил leaf fingerprint под тем же проверенным CA.
- **SQL SIGKILL:** 15s downtime, тот же volume; 10 последовательных healthy
  probes после restart за **24,783s** при budget 30s.
- **Cache SIGKILL:** 15s downtime и cold restart; recovery **3,845s** ≤30s.
- **Broker SIGKILL:** 15s downtime; outbox catch-up **20,288s** ≤60s,
  application supervisor восстановил workers после transport failures.
  HTTP 202 продолжали приниматься в SQL outbox во время недоступного broker.
- **App SIGKILL:** вторая replica обслуживала запросы; failover probe **6,13ms**,
  restart **2,336s**. Ошибка одного HTTP запроса включена в таблицу.
- **Reconcile:** все **10 761 подтверждённых HTTP 202 IDs** найдены с effects=1.
  Всего SQL сохранил **10 763 операции**: две завершились commit при
  неопределённом для клиента ответе. Они также обработаны ровно с одним effect.
  Повторный запрос с прежним ID не продублировал effect. At-least-once delivery
  и неопределённый исход HTTP при disconnect не выдаются за exactly-once HTTP.
- **Backup/restore:** writers quiesced, pg_dump -Fc восстановлен в новую БД;
  все отсортированные rows совпали по count/content/hash. Restored app прошёл
  TLS request; RPO=0 для quiesced snapshot, RTO **5,852s** ≤60s.
  Сохранён сам [backup](operations/baseline-v1/backup.dump), 35 037 bytes.
- **Migration recovery:** процесс остановлен после committed DDL до history
  finalize; sleeping SQL session этой job завершена оператором. Повторный Up
  заблокирован dirty marker. После проверки schema/rows выполнен Resolve с
  exact checksum; следующий Up — noop. Recovery **4,703s** ≤60s.
- **Shutdown/cleanup:** graceful exit=0, OOM=false; созданные этим run
  containers/network/volumes удалены. Не относящиеся к стенду ресурсы сохранены.

## Исправления стенда и проверки

Smoke выявил и закрыл: root-owned RabbitMQ cookie из раннего diagnostics probe;
конкурирующие outbox dispatchers и медленный drain; гонку Docker stats с
удалением replica. Probe выполняется от rabbitmq; dispatcher использует
FOR UPDATE SKIP LOCKED и bounded batches до 32 IDs, worker идемпотентен;
stats получает running containers и сохраняет только роли своего run.
Короткий smoke не подменяет full acceptance. Реальные повторные сценарии и
negative report/artifact gates проверяют исправления. Четыре отклонённых smoke
сохранены локально; их [manifest summaries](operations/rejected-runs-v1.json)
содержат причины отказа и успешный cleanup. Полных отклонённых full runs не было.

Проверены Windows root tests/vet, adapters tests, integration vet и сборка app;
Linux vet/race compilation app, golangci-lint **0 issues**; Actionlint;
**16 Node tests**, включая отрицательные SLO/coverage/recovery/artifact cases;
docs checker и onboarding в fresh module; Gitleaks **0 leaks**.
Library/frontend/dependency sources не менялись; повторный общий browser,
service conformance всех комбинаций и vulnerability scan в этом шаге не заявляются.
Linux git archive с pinned core.autocrlf=true независимо воспроизвёл exact
archive SHA-256 принятого Windows RC.

## Evidence и повторение

[Raw-derived report](operations/baseline-v1/report.md),
[events](operations/baseline-v1/events.json),
[requests](operations/baseline-v1/requests.ndjson.gz),
[resources](operations/baseline-v1/resources.json.gz),
[checksums](operations/baseline-v1/SHA256SUMS.json).
Gzip сохраняет raw bytes после декомпрессии. Checksum связывает backup именно с
восстановленным артефактом. Независимая проверка заново применяет все gates и
сверяет реконструированный report:

```bash
node release/operations/verify.mjs docs/operations/baseline-v1
```

Полный повтор — [operations runbook](guide/operations.md). Weekly/manual/reusable
workflow сохраняет evidence без private keys/config/tokens. Hosted runs требуют
push владельцем и не выдаются за выполненные локально.

Границы: один Docker host, одна интегрированная конфигурация, 10 минут и
синтетический workload; нет HA/power/disk-loss/PITR/WAN доказательств. Временный
DLQ — tmpfs, poison-message durability покрывается прежними queue acceptance
tests и требует persistent volume в production. Starters с process-local state
не получают multi-replica гарантии этого reference app. Новый RC/source требует
нового frozen объекта и полной приёмки. **12.1/A4 остаётся за hosted acceptance
владельца; 13.3 — API/compatibility review, release checklist и подпись владельца.**
