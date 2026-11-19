# Release checklist v1.0.0 — ожидает владельца

Target: **v1.0.0, unpublished draft**. Accepted RC: **v0.1.0-rc.1**,
`90b2c3bf936c5d7f67eb2a4b9216d438b1f40739`.
Machine-readable binding — [freeze-v1.json](../release/freeze-v1.json).
Эта страница не является подписью владельца и не разрешает публикацию.

## Подготовленная локальная область

- API inventory: 27 public packages, 991 declarations/selectors;
  [scope/experimental matrix](guide/stability.md) и [baseline](../release/api-v1.json).
- [Upgrade RC1→v1 / ранние 0.x](guide/upgrade-v1.md), стабильные config/CLI/storage
  contracts; release tools проверяют pins вложенных modules и external consumer.
- Известные локальные P0 из повторного аудита закрыты regression/service tests:
  handler panic, SQL reserve/Nack CAS, durable DLQ before Ack, migration lock/dirty.
  Evidence — [8–9](stages-8-9-acceptance.md), [10.2](stage-10-2-acceptance.md),
  [security audit](guide/security-audit.md). Наличие новых GitHub issues проверяет
  владелец перед подписью; local registry не заменяет triage.
- [Поставка 13.1](stage-13-1-acceptance.md) и [операционная приёмка 13.2](stage-13-2-acceptance.md).
  Runtime promotion допускает только metadata versions, не неявные исправления.
- Локальные tests, security scans и frozen consumer приняты; stable draft из
  `b58e226` принят 19.11.2026: воспроизводимы три runtime manifests, прошли
  version-tag consumer и production smoke. [Distribution evidence](releases/v1.0-proposal/distribution/README.md)
  фиксирует source/artifact identity. Draft artifacts не считаются опубликованным
  релизом; hosted CI и подпись должны относиться к точному reviewed source.

## Обязательное закрытие владельцем

- [ ] Push проверенного source в `https://github.com/chitushka/framework.git`;
  hosted CI для точного commit, все matrix children и aggregate success.
- [ ] Сохранены unexpired artifacts четырёх Go/OS matrix children, coverage,
  fuzz, frontend/security/service/docs/REST/browser/performance/compatibility.
- [ ] `Required quality gate` включён required check для master, strict update,
  enforcement для admins; bypass/release permissions ограничены.
- [ ] Отсутствуют открытые P0; security gates и policy exceptions просмотрены.
  Нет blanket waivers, skipped/cancelled jobs не принимаются за success.
- [ ] Владелец просмотрел API, experimental scope, upgrade guide, exact source,
  SBOM/provenance/checksums и подписал checklist для этих identity.
- [ ] Получено отдельное разрешение публикации root/nested tags, image registry
  и GitHub Release. Push/tag/artifact actions выполняет владелец либо агент
  после явного разрешения.

Подпись: **не предоставлена**. Commit выпуска: **определяется после hosted CI**.
Разрешение публикации: **не предоставлено**.

## Проверка готовности и передача

```bash
node release/readiness.mjs
```

Ненулевой exit до закрытия внешних условий ожидаем. Команда не подписывает и
не публикует. Шаблон [approval-v1.example.json](../release/approval-v1.example.json)
намеренно пустой; заполненный receipt храните вне source commit, например в
`.cache/release-approval-v1.json`, чтобы подпись могла связать точный tested SHA.
Передайте receipt вторым аргументом readiness после проверки GitHub и решения
владельца. JSON — запись attestations, а не самостоятельное доказательство
авторства GitHub/подписанта; поля нельзя заполнять симулированными success.

Receipt связывает target/API/RC/source commit, run URL и результаты каждого job,
artifact IDs/expiry, branch restrictions, owner login/UTC/review flags и explicit
publication permission. `checklistSHA256` — SHA-256 JSON object в указанном порядке:
`{targetVersion, apiSHA256, candidate, sourceCommit}` из freeze и tested commit.
Readiness отвергает stale SHA, missing/failed/skipped matrix, expired artifacts,
ослабленные required checks, отсутствие подписи/разрешения.

После одобрения выпускаются immutable tags `v1.0.0`, `adapters/v1.0.0`,
`examples/advanced-admin/v1.0.0` для одного reviewed commit. Public version install,
published image digests и Release assets сверяются с подписанным manifest.
Не перемещайте tags при дефекте: следующий patch либо новый RC с новой приёмкой.
