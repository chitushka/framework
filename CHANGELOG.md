# Changelog

## Unreleased — candidate v0.1.0-rc.1

Первый подготовленный RC; tag и artifacts ещё не опубликованы. API до v1.0
может меняться; перенос с учебного scaffold требует проверки guide.

- Ограниченные HTTP resources, proxy trust/host policy, sessions/CAS/rotation,
  CSRF, Argon2id authentication и проверка внешних JWT.
- SQL conformance, межпроцессные migration locks и recovery, Active Record,
  проверяемые генераторы, cache/queue adapters и bounded shutdown/export.
- Basic, Advanced Admin с SQL/RBAC и REST starters; проверяемая документация,
  Windows/Linux tests, real-service/browser gates, fuzz/coverage и performance
  baseline. Доказательства и ограничения — `docs/stage-*-acceptance.md`.
- MIT/NOTICE, dependency/asset audit, non-root runtime images, SPDX SBOM,
  build provenance, reproducibility и изолированная version-tag installation.

Hosted CI/required checks 12.1/A4, эксплуатационная приёмка 13.2 и freeze/public
release 13.3 остаются отдельными условиями. Этот changelog не объявляет v1.0
или подтверждённую production-ready поставку.
