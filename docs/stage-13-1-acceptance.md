# 13.1 — локальная приёмка поставки

Дата: 15.11.2026. Candidate: **v0.1.0-rc.1**, platform **linux/amd64**.
Исходники зафиксированы commit `90b2c3bf936c5d7f67eb2a4b9216d438b1f40739`;
source archive SHA-256:
`95d79daedbda472a6ebdf8a0a2a5e8b984b5ba9888d1fc5ed1b8cb416f3386d8`.
Commit приёмки добавляет evidence/PLAN; проверенный candidate остаётся привязан
к указанному source commit. Это локально подготовленный RC; публичная поставка
и production-ready v1.0 этим отчётом не объявляются.

## Что подготовлено

- Собственный код — [MIT](../LICENSE), по выбору владельца. Отдельные LICENSE
  сохранены в nested modules. [NOTICE](../NOTICE) определяет границу стороннего
  кода. [SECURITY](../SECURITY.md) содержит согласованный private email;
  [CONTRIBUTING](../CONTRIBUTING.md), [CHANGELOG](../CHANGELOG.md) и
  [release policy](guide/releases.md) описывают вклад, версии и условия выпуска.
- Audit 48 production npm packages: version/integrity из package-lock,
  оригинальные LICENSE/COPYING/NOTICE, Apache/BSD/ISC/MIT тексты, upstream
  `app.js.LICENSE.txt` для precompiled AdminKit JS. Байты AdminKit 3.5.0 CSS/JS
  и notices закреплены SHA-256; CSS содержит inline Bootstrap SVG, внешних
  font/image/style references нет. Release Basic/Admin содержат только assets
  своего Vite entry; demo Vue/Chart.js/map JavaScript в них отсутствует.
- `release/Dockerfile`: scratch runtimes, UID/GID 65532, static binaries,
  CA bundle и его upstream copyright, Go/standard-library/npm notices.
  Собственный MIT не заменяет dependency licenses. Старый Admin runtime target
  также дополнен LICENSE/NOTICE и dependency notices.
- `release/prepare.mjs`: clean git archive, pinned builders/scanner, шесть OCI
  builds, SPDX и mode=max provenance, content/subject verification, fresh
  version-tag consumer и production smoke. Release workflow запускает это
  после reusable CI и сохраняет candidate artifacts без публикации.
- Требования nested modules переведены с `v0.0.0` на `v0.1.0-rc.1`, чтобы
  consumers могли разрешать dependency graph без workspace replaces.

## Воспроизводимость и artifacts

Две сборки, первая Basic в каждой серии с `--no-cache`: pinned Go 1.26.11,
CGO=0, trimpath, buildvcs=false, пустой build ID, SOURCE_DATE_EPOCH source commit,
OCI rewrite-timestamp. Сравнивались полные runtime manifest digests, включая
config, binary, assets, notices, CA и file metadata. Все три совпали.

| Starter | OCI bundle с attestations | SPDX packages | Runtime digest, SHA-256 |
| --- | ---: | ---: | --- |
| Basic | 4,06 MiB | 54 | `147c3fd03a6dca7f718256a8bdd267506c5bbe661635b8fa401e197f331f7f98` |
| Admin | 5,46 MiB | 63 | `e05f27577a88d94aa3d4b04766fab0edc8b66e4faf9e43d001e12bfe67e03759` |
| REST | 2,98 MiB | 6 | `d0b398b26aa50448fa25f0ba6b1f8f05bee836c99a158a45d225652d6a14e77b` |

SPDX package counts включают metadata и полный npm supplement, в том числе
пакеты, не вошедшие в выбранные starter assets; это не число используемых runtime
dependencies. BuildKit включает supplement в итоговый Basic/Admin SBOM.

[Release manifest](releases/v0.1.0-rc.1/release.json),
[checksums](releases/v0.1.0-rc.1/SHA256SUMS) и
[smoke result](releases/v0.1.0-rc.1/smoke.json) сохранены с полными digests.
Большие OCI/source archives и predicates находятся локально в
`.cache/releases/v0.1.0-rc.1-UJSWWK`; новый прогон создаёт новый каталог,
последний успешный путь записывается в `.cache/releases/latest.txt`.
SHA256SUMS и независимое повторное чтение обеих серий OCI проверены.

Attestations содержат времена сборки, поэтому OCI index/tar могут отличаться
между сериями. Docker load/container identity проверяется по index digest первой
серии; reproducibility — по вложенному runtime manifest digest. Provenance
локальные unsigned, без удостоверения личности builder или заявления SLSA level.
Проверена одна platform; подписанная hosted provenance не заявляется.

## Установка и smoke

Из snapshot создан отдельный Git repository с локальными version tags root,
`adapters/` и `examples/advanced-admin/`. Официальный x/mod/zip сформировал private
file GOPROXY. Внешний consumer с новым module cache установил root и конкретные
Redis/RabbitMQ/OTel packages по version, прошёл test и `go mod verify`, затем
установил и собрал Admin и повторил verify. Checkout replacements в consumer
отсутствуют; public checksum validation остальных dependencies включена.
Вложенный Admin остаётся source scaffold, а не `go install ...@version` tool.

Smoke запускал именно проверенные OCI indexes: production, read-only rootfs,
UID/GID 65532, cap-drop=ALL и no-new-privileges. Подтверждены Basic credentials,
Secure cookies и assets; Admin migrate/seed/login на временном PostgreSQL 17;
REST 401 без token и create/read/update/delete с внешним Ed25519 JWT; SIGTERM
всех трёх processes завершился exit=0. Порты привязаны только к localhost,
SQL data временные, containers/network удалены. Test secrets не входят в images
и source archive.

## Остальные проверки и границы

Windows Go vet/tests root, adapters, Admin и release checker; изолированное
onboarding; Linux vet/race тех же modules, compile integration, docs/run, lint
ядра — успешно. Node suite: 12/12, включая artifact tampering, wrong subject,
missing provenance, root runtime, asset traversal и license/asset drift.
Gitleaks: 0 leaks. Govulncheck `-test`, все пять application/tool modules:
0 affected calls; сообщения о неиспользуемых vulnerable packages/modules не
подавлялись. Исключения security gates не добавлялись.

Core coverage 84,94% при пороге 80%; auth 86,60% ≥85%, session 87,31% ≥85%,
security 94,73% ≥90%. HTTP/SQL runtime code и frontend entry sources не менялись;
performance soak и browser E2E повторно в этом инкременте не измерялись.
Release workflow зависит от полного CI, включая service/browser/performance jobs.

Шаг **13.1 принят локально**. Это проверка tagged distribution через частный proxy,
а не установка опубликованного public tag. Main repository/public tags и registry
не публиковались. 12.1/A4 остаётся за hosted matrix/required checks владельца;
13.2 — TLS, SLO/load, backup/restore/recovery/key rotation; 13.3 — API freeze,
подпись checklist и отдельная разрешённая публикация.
