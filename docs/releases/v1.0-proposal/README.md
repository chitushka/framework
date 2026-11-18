# v1.0.0 — локальная подготовка выпуска

Это каталог evidence для unpublished draft, а не опубликованный stable release.
Принятый RC1 и его operational artifacts сохраняются отдельно и не переименовываются.

Область API и promotion: [stability](../../guide/stability.md).
Условия выпуска и незаполненная подпись: [checklist](../../release-checklist-v1.md).
Проверка текущих условий: `node release/readiness.mjs` из корня.

Сохранены [compatibility result](compatibility.json),
[local validation](local-validation.json) и gzip-логи успешных Windows/Linux,
Node/docs/frontend/security/compatibility проверок в `validation/`. SHA-256
в local-validation считается по распакованному UTF-8 логу; каждое выполнение
сохраняет собственный source commit. Runtime/dependency fingerprint связывает
проверки library с metadata-only promotion RC→v1.

`complete: false`: integration/browser и stable draft build пока блокирует
неотвечающий Docker Engine. Отчёт не выдаёт их за пройденные проверки.
[Состояние 13.3](../../stage-13-3-acceptance.md) содержит область evidence и
оставшиеся условия. Hosted CI/branch restrictions и signoff могут закрываться
только фактическими данными GitHub и решением владельца.
