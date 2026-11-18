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
digest в двух metadata paths. Реальные scanner canaries доказывают, что credential
в том же файле, то же значение в другом поле или application path остаются
находками. Правила credentials не отключены. Worktree и полная Git history
проверены; `govulncheck -test` пяти modules не выявил уязвимых вызываемых symbols.

## Локальная валидация

Windows vet/tests и REST process E2E прошли. Linux vet/race, lint пяти modules
(0 issues), восемь fuzz smoke targets и negative coverage/fuzz checks прошли.
Фактическое statement coverage: core **84.38%**, auth **86.60%**, session
**87.31%**, security **94.73%**. Синтетические profiles negative tests не являются
измеренным покрытием проекта.

Прошли 20 Node tests, docs/onboarding (после добавления отчёта: 68 документов,
647 links, 3 source fences),
frontend lint/build/tests/audit и проверки security. Frozen consumer, current/RC
API и runtime comparison прошли на чистом `079d362`.

Service/browser сценарии и stable draft build пока не приняты: при старте
integration dependencies Docker Engine вернул HTTP 500 и перестал отвечать.
Application tests этого прогона не запускались. Повторная приёмка и упаковка
сырых логов выполняются после восстановления Engine; инфраструктурный сбой
не считается успешной проверкой приложения.

## Оставшиеся условия выпуска

1. Завершить service/browser проверки и локальную воспроизводимую сборку draft.
2. По отдельному разрешению отправить reviewed source в
   `https://github.com/chitushka/framework.git` и получить hosted CI success
   для точного SHA со всеми matrix children и unexpired artifacts.
3. Включить `Required quality gate` для master со strict/admin enforcement
   и ограничениями bypass/release permissions; закрыть 12.1/A4.
4. Получить подпись владельца для exact API/upgrade/security/checklist identity.
5. После отдельного разрешения опубликовать immutable root/nested tags, images
   и Release assets, проверить public installation и digests.

[Checklist](release-checklist-v1.md) содержит незаполненную подпись и формат
receipt. Правило локальных commits и отдельного push/publication из
[PLAN](../PLAN.md) сохраняется. Репозиторий GitHub доступен, `origin` настроен;
на момент подготовки удалённый репозиторий пуст, отправок агент не выполнял.
