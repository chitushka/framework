# Эксплуатационная приёмка RC

[Протокол v1](../operations/protocol-v1.md) задаёт объект, нагрузку и SLO до
измерений. Это PostgreSQL + Redis cache + RabbitMQ и две версии эталонного
приложения за проверяемым TLS nginx proxy. HTTP requests, SQL outbox, confirms,
идемпотентный worker и application supervisor работают на frozen framework RC.
Application code — `integration/operations/main.go`; runner и gates —
`release/operations`. Dockerfile накладывает только это приложение на проверенный
архив RC. Код framework/adapters берётся из архива, а не текущего checkout.

## Запуск

Нужны Node 22+, Git, tar, Linux Docker Engine/Desktop, доступ к pinned images и
Go modules. Из чистого commit:

```bash
node --test scripts/operations-report.test.mjs
node release/operations/run.mjs full
```

На машине с принятой поставкой используется `.cache/releases/latest.txt`.
Можно явно передать каталог третьим аргументом. При отсутствии этого файла
runner экспортирует frozen commit через `git archive` и проверяет тот же SHA-256
из принятого release manifest. Исходный архив 13.1 был экспортирован с
`core.autocrlf=true`; runner закрепляет этот режим и для Linux, сохраняя exact
bytes принятого RC, включая LF shell scripts по `.gitattributes`.
Для fresh clone нужен `git fetch --unshallow`
либо clone с полной историей. Runner не меняет tags и не публикует artifacts.

Для отладки dirty checkout:

```bash
node release/operations/run.mjs smoke
```

Smoke не даёт `accepted=true`. Full — 600s × 20 POST/s, дополнительно startup,
backup/restore и recovery. Не запускайте рядом competing benchmark jobs.
Результат — `.cache/operations/chitu-ops-*/report.md`, raw requests/resources,
scenario events, manifest с identity и hashes, custom-format SQL backup.
Неудачный run сохраняется. Отсутствующие evidence, SLO failure и ошибка cleanup
возвращают ненулевой exit. Следующий запуск использует новые volumes/network.
Независимая повторная проверка raw observations и backup checksum:

```bash
node release/operations/verify.mjs .cache/operations/chitu-ops-RUN_ID
```

Verifier также читает gzip-сжатые artifacts из опубликованного отчёта.

## Recovery и rolling runbook

1. Сохранность: HTTP 202 выдаётся после SQL insert. Outbox dispatcher публикует с
   confirms; dispatcher блокирует до 32 rows через FOR UPDATE SKIP LOCKED и
   публикует один bounded batch, чтобы replicas не конкурировали за те же rows.
   Crash до commit отметок enqueued допускает повтор. Worker меняет effect
   только при effects=0. Равенство всех accepted IDs и SQL effects проверяется
   после нагрузки; повторный HTTP с тем же ID не дублирует effect.
2. Rolling: expand migration добавляет column с default. Новая replica начинает
   писать column; старая продолжает работать. Сначала новая replica входит в
   upstream, затем старая удаляется из upstream, проходит SIGTERM drain и
   возвращается после expand как проверка rollback. Contract migration требует
   отдельного rollout после удаления старого кода.
3. JWT: atomic replacement файла keys.json; обе replicas загружают валидный ring.
   Overlap ограничен AcceptUntil; после перевода issuer/client на новый kid
   старый удаляется. Invalid ring сохраняет последний валидный набор. TLS leaf
   заменяется nginx reload под тем же CA; клиент проверяет CA/hostname и смену
   fingerprint. Private keys/tokens остаются в ignored config directory.
4. Зависимости: SIGKILL только временных SQL/cache/broker контейнеров; SQL и
   broker поднимаются с прежними volumes, cache — холодный. Shared SQL outbox
   ограничивает потерю принятых задач при недоступном broker. Worker fatal
   останавливает группу; application supervisor делает bounded restart.
5. Backup: proxy quiesce, остановка writers/workers, pg_dump -Fc. Restore идёт
   в новую database; сверяются все sorted rows, затем restored app проходит TLS
   readiness. RPO=0 относится к quiesced snapshot, а не непрерывному PITR.
6. Migration: специальный nontransactional script коммитит DDL и засыпает до
   финализации history. После SIGKILL оператор при необходимости завершает
   только sleeping SQL session этой job; Up обязан блокироваться по dirty.
   Оператор проверяет таблицу и её rows, вызывает Resolve с exact checksum и
   проверяет повторный Up. Resolve не заменяет анализ фактической production DDL.

Все имена уникальны; host port — только TLS на localhost. Cleanup удаляет только
созданные run контейнеры/network/volumes. Raw backup содержит исключительно
синтетические operation IDs. Runtime app — UID 65532, read-only, dropped caps,
bounded SQL/HTTP/cache/queue deadlines. Временный DLQ на tmpfs достаточен для
этого стенда без poison jobs; для production FileDeadLetters требует persistent
volume, согласно [queue guide](queues.md).

## Автоматизация и ограничения

`operations.yml` поддерживает manual/reusable и weekly full run, сохраняет
evidence без config/private keys. Workflow проверяет frozen объект из протокола;
он не принимает автоматически новый RC, подготовленный из другого commit.
При 13.3 владелец сверяет identity выпуска с operational report. Новый candidate
требует нового frozen protocol/manifest и полного прогона до публикации.
Hosted execution и required checks настраиваются владельцем после push; локальный
успех их не подменяет. [Release policy](releases.md) сохраняет отдельные условия
для API freeze и публикации.

Протокол измеряет одну конфигурацию на одном Docker host, 10 минут, синтетический
workload. Backend traffic внутри private network — plaintext. HA, power/disk
failure, PITR, WAN, длительные resource leaks и combinations остальных backends
не подтверждены. Basic/Admin/REST starters с process-local sessions/data не
становятся horizontally scalable из-за приёмки эталонного приложения.
