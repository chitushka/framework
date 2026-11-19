# v1.0.0 — локальная подготовка выпуска

Это каталог evidence для unpublished draft, а не опубликованный stable release.
Принятый RC1 и его operational artifacts сохраняются отдельно и не переименовываются.

Область API и promotion: [stability](../../guide/stability.md).
Условия выпуска и незаполненная подпись: [checklist](../../release-checklist-v1.md).
Проверка текущих условий: `node release/readiness.mjs` из корня.

Сохранены [compatibility result](compatibility.json),
[local validation](local-validation.json) и gzip-логи успешных Windows/Linux,
Node/docs/frontend/security/compatibility/service/browser/workflow проверок в
`validation/`. SHA-256
в local-validation считается по распакованному UTF-8 логу; каждое выполнение
сохраняет собственный source commit. Runtime/dependency fingerprint связывает
проверки library с metadata-only promotion RC→v1.

`complete: true`: все 11 локальных quality checks приняты. После запуска Docker
Desktop 19.11.2026 прошли integration/race (29 основных тестов без skips),
Basic E2E 3/3, Admin SQL/race на PostgreSQL/MySQL и browser E2E 4/4, actionlint.
Stable draft из `b58e226` также принят локально: три пары runtime manifests
воспроизводимы, version-tag consumer и production smoke прошли.
[Distribution evidence](distribution/README.md) содержит manifest, checksums,
smoke, независимую проверку и полный лог. Поле `complete` описывает quality
checks, а не готовность к публикации.
[Состояние 13.3](../../stage-13-3-acceptance.md) содержит область evidence и
оставшиеся условия. Hosted CI/branch restrictions и signoff могут закрываться
только фактическими данными GitHub и решением владельца.
