# Этап 13.3 — подготовка API freeze и v1.0.0

Статус: **локальная подготовка, выпуск не завершён**. `v1.0.0` — unpublished
draft. Этап 13.3, 12.1 и A4 остаются `[~]` до фактической hosted приёмки,
настройки required checks, подписи владельца и разрешённой публикации.

## Реализованный результат

- Зафиксированы 27 public packages и 991 declarations/selectors в
  [API baseline](../release/api-v1.json). `go run ./tools/apifreeze` сверяет
  реальные экспортированные типы, константы, generic constraints, fields/tags,
  method sets и promoted methods; private layout не входит в обещание API.
  Unit tests проверяют выявление изменений и допустимость private rename.
- [Stability matrix](guide/stability.md) задаёт стабильную область, обязательства
  конфигурации/CLI/storage и экспериментальные границы. Подготовлен
  [upgrade guide](guide/upgrade-v1.md), включая ранние несовместимые 0.x.
- [Compatibility suite](../compatibility/public_test.go) использует только
  публичный API: HTTP/SQL, session format/CAS/cookies, queue JSON/DLQ/replay,
  config, password PHC, migration checksum, CLI/assets/mass assignment.
- `node release/compatibility.mjs` независимо извлекает принятый RC из Git,
  проверяет точный source checksum и API, сравнивает 95 runtime/dependency
  sources и запускает неизменённые fixtures во внешнем consumer с отдельными
  GOPROXY/module cache. Проверяются root и три SDK modules, checksums зависимостей
  и сборка Admin. Promotion допускает только собственные version pins RC→v1;
  изменение runtime или внешних dependencies требует новой RC-приёмки.
- Release tools подготавливают локальный stable draft с reproducibility,
  SBOM/provenance/checksums, production smoke и установкой v1 по version tag
  через изолированный file proxy. Эти проверки не заменяют public install
  после публикации. Принятый RC и его operational evidence не переименовываются.
- [Readiness](../release/readiness.mjs) сверяет локальное evidence и отдельный
  owner receipt, связанный с target/API/RC/source SHA. Отсутствующий hosted job,
  неполная matrix, expired artifact, ослабленные restrictions или отсутствие
  подписи/разрешения оставляют выпуск заблокированным. Negative tests проверяют
  эти случаи; синтетические fixtures не записываются как реальные одобрения.

## Принятый объект и security

Основание promotion: `v0.1.0-rc.1`, revision
`90b2c3bf936c5d7f67eb2a4b9216d438b1f40739`, source archive SHA-256
`95d79daedbda472a6ebdf8a0a2a5e8b984b5ba9888d1fc5ed1b8cb416f3386d8`.
[Операционная приёмка 13.2](stage-13-2-acceptance.md) независимо проверяется
readiness; новая сборка v1 имеет отдельную source/artifact identity.

Известные локальные P0 закрыты regression/service tests; registry с evidence
находится в [freeze](../release/freeze-v1.json). Это не утверждение об отсутствии
новых GitHub issues: актуальный triage входит в owner checklist.

`npm audit` сохраняет исходный отчёт и проверяет свежие canonical GitHub metadata
для withdrawn advisory; активные high/critical продолжают блокировать gate.
Реальная проверка GHSA-fc5q-39xc-vjpf подтвердила отзыв advisory; последующий
registry audit уже вернул ноль findings. Policy exceptions остаются пустыми.
[Canonical advisory](https://github.com/advisories/GHSA-fc5q-39xc-vjpf).

Gitleaks классифицирует только точное публичное поле `apiSHA256` с зафиксированным
digest в трёх metadata paths, включая stable draft manifest. Реальные scanner
canaries для каждого пути доказывают, что credential в том же файле, то же
значение в другом поле, иной digest и application path остаются находками.
Правила credentials не отключены. Worktree и полная Git history
проверены; `govulncheck -test` пяти modules не выявил уязвимых вызываемых symbols.

## Локальная валидация

Windows vet/tests и REST process E2E прошли. Linux vet/race, lint пяти modules
(0 issues), восемь fuzz smoke targets и negative coverage/fuzz checks прошли.
Фактическое statement coverage: core **84.38%**, auth **86.60%**, session
**87.31%**, security **94.73%**. Синтетические profiles negative tests не являются
измеренным покрытием проекта.

Прошли 20 Node tests, docs/onboarding с исполнением трёх source fences,
frontend lint/build/tests/audit и проверки security. Frozen consumer, current/RC
API и runtime comparison повторно прошли 19.11.2026 на чистом `26a25fe`.

19.11.2026 Docker Desktop запущен, Engine 29.2.0 отвечает. Полный integration
suite прошёл под race detector: 29 основных тестов, без skips и failures,
130,576s исполнения тестов. Проверены PostgreSQL/MySQL, Redis, Memcached,
RabbitMQ, OpenTelemetry, сбои зависимостей и process/crash recovery. Basic
Chromium E2E — **3/3**, включая no-JavaScript mobile и production HTTPS;
Admin SQL/race на PostgreSQL/MySQL и Chromium E2E — **4/4**, включая RBAC,
отказ повышения прав и сохранение данных после restart. Actionlint прошёл.
Эти проверки выполнены на `26a25fe`; тестовые containers, networks и volumes
удалены штатными runners. Исходный сбой Engine не засчитывается как проверка.

Все 11 локальных quality checks и их gzip-логи сохранены в
[local validation](releases/v1.0-proposal/local-validation.json), `complete: true`.

## Локальная приёмка stable draft 19.11.2026

`node release/prepare.mjs` успешно завершён из чистого source commit
`740a769f7b039fb04dc5fecf95bde2e8e5207dc7`. Source archive SHA-256:
`118bdd6c7cfce12fd19115542c49415297f981a15baa0b23bb35ab8578426d0c`.
Commit с результатами добавляет evidence; runtime/dependencies принятого RC
не меняются. В `740a769` исправлена только политика LF для Windows checkout,
диагностика CI и соответствующая инструкция для contributors.

Две сборки каждого starter, Basic с `--no-cache` в каждой серии; все три
runtime manifest digests совпали. Независимая повторная инспекция обеих серий
OCI подтвердила blob hashes/sizes, SPDX/provenance subjects, UID/GID и source/version
labels. Source archive повторно сверен с Git, все SHA256SUMS проверены.

| Starter | OCI с attestations | SPDX packages | Runtime digest, SHA-256 |
| --- | ---: | ---: | --- |
| Basic | 4,06 MiB | 54 | `12151f671592700c7b040ac2bdb1d909ee92b3d84462e0e7dd490dc8d7223f86` |
| Admin | 5,46 MiB | 63 | `837cd63095fe9b1c3b2c3137df56b9ef4180e786019de4ddf5f8dda231ced507` |
| REST | 2,98 MiB | 6 | `ed047110173efecd439b212f1087dba0c79dc35a73c5e5b323d62fd1052fad15` |

Внешний consumer установил root/adapters/Admin по `v1.0.0` через изолированный
file GOPROXY, выполнил неизменённые compatibility fixtures, `go mod verify`
и сборку Admin. Production smoke использовал именно проверенные OCI indexes:
read-only rootfs, UID/GID 65532, Basic login/assets/Secure cookies, Admin
PostgreSQL migrations/seed/login, REST 401 и external JWT CRUD, SIGTERM exit=0
всех трёх processes. Test containers и network удалены.

[Manifest, checksums, smoke, independent verification и полный gzip-лог](releases/v1.0-proposal/distribution/README.md)
сохранены в репозитории. Большие source/OCI archives, predicates и module-check
остаются локально в `.cache/releases/v1.0.0-UsT3jq`; latest.txt указывает этот
успешный прогон. Проверена одна platform `linux/amd64`. Provenance локальные
unsigned; локальная установка по версии не заменяет public installation.

## Hosted приёмка: устранённые инфраструктурные дефекты

Первый [CI run](https://github.com/chitushka/framework/actions/runs/44520088465)
выявил преобразование всех 254 Go-файлов в CRLF при Windows checkout. В `740a769`
закреплён LF; свежий checkout с `core.autocrlf=true` подтвердил 254 ошибки до
исправления и 0 после. В [повторном CI](https://github.com/chitushka/framework/actions/runs/44521185098)
все четыре Go/OS children, lint, service/browser, security и остальные обычные
проверки прошли: 18 основных jobs. Performance measurements завершились exit=0,
но сборщик и uploader не смогли прочитать root-owned `runtime.json` с `0600`.
Этот run не принят.

Публичные performance JSON переведены на `0644`; настоящий reader с UID/GID
65534 воспроизвёл ошибку до исправления и прочитал результат после него.
Регрессия добавлена перед каждым performance run; vet/lint performance harness
и три Node report gate tests прошли. Полная hosted приёмка требует нового
успешного run для исправленного source и настройки required checks.

## Оставшиеся условия выпуска

1. По отдельному разрешению отправить reviewed source в
   `https://github.com/chitushka/framework.git` и получить hosted CI success
   для точного SHA со всеми matrix children и unexpired artifacts.
2. Включить `Required quality gate` для master со strict/admin enforcement
   и ограничениями bypass/release permissions; закрыть 12.1/A4.
3. Получить подпись владельца для exact API/upgrade/security/checklist identity.
4. После отдельного разрешения опубликовать immutable root/nested tags, images
   и Release assets, проверить public installation и digests.

[Checklist](release-checklist-v1.md) содержит незаполненную подпись и формат
receipt. Правило локальных commits и отдельного push/publication из
[PLAN](../PLAN.md) сохраняется. Репозиторий GitHub доступен, `origin` настроен;
19.11.2026 владелец отдельно разрешил push, hosted CI и required checks.
Коммиты `5e39a56` и исправление Windows CI `740a769` отправлены в master;
публикация tags/images/Release и подпись checklist требуют отдельного решения.
