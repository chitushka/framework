# Release policy и локальная подготовка RC

Собственный код — [MIT](../../LICENSE); сторонние notices сохраняются отдельно
по [NOTICE](../../NOTICE). Приватный канал — [SECURITY](../../SECURITY.md), правила
изменений — [CONTRIBUTING](../../CONTRIBUTING.md), история — [CHANGELOG](../../CHANGELOG.md).

## Версии и условия публикации

`release/version.json` задаёт текущий candidate и platform. До v1.0 выпускаются
только RC, без обещания стабильности API. Семантика версий/deprecation —
[support](support.md). Tag корневого модуля — `vX.Y.Z-rc.N`; вложенные Go-модули
используют префикс каталога: `adapters/vX.Y.Z-rc.N` и
`examples/advanced-admin/vX.Y.Z-rc.N`. Их requirements должны указывать ту же
версию root. Workspace replaces помогают разработке, но не действуют у consumer.
Admin — source scaffold: используйте checkout или `go get` и `go build` внутри
своего модуля; `go install ...@version` для модуля с replace не поддерживается.

Один candidate всегда соответствует одному source commit. После изменения
source, dependencies или build recipe увеличьте RC number перед публикацией;
опубликованные tags не перемещаются. Changelog описывает поведение, breaking
changes и upgrade steps. Исправления stable после v1.0 — patch; совместимые
возможности — minor; удаление API требует deprecation и major по support policy.

Публикация требует полного CI aggregate success, hosted acceptance 12.1/A4,
операционной приёмки 13.2, закрытых блокирующих дефектов и подписи владельца по
13.3. Локальные checks не заменяют эти условия. Push tags, registry images и
GitHub Release — отдельное разрешённое владельцем действие. Workflow только
готовит и сохраняет artifacts; credentials публикации ему не нужны.

## Подготовка из чистого commit

Нужны Git, tar, Node 22+ и Docker Desktop/Engine с Buildx, OCI exporter и
containerd image store для локального OCI load/smoke. На
Windows Docker должен использовать Linux containers. Из корня чистого checkout:

```bash
node release/prepare.mjs
```

Команда не меняет tags рабочего репозитория. Она создаёт уникальный каталог в
`.cache/releases`, экспортирует только tracked source через `git archive`,
собирает три runtime images, затем повторяет сборку с `--no-cache`. Обе серии
используют один pinned Go toolchain, asset builder, npm lock, SBOM scanner digest,
linux/amd64, version/revision labels и `SOURCE_DATE_EPOCH` source commit.
`CGO_ENABLED=0`, `-trimpath -buildvcs=false -ldflags='-s -w -buildid='` исключают
host paths и изменяемые VCS/build IDs. Runtime images — scratch, UID/GID 65532,
статический binary, CA certificates, LICENSE/NOTICE, dependency notices и только
достижимые Vite assets Basic/Admin. Shell/package manager в runtime отсутствуют.
Проверенный image можно собрать отдельно (без приёмки/attestations):

```bash
docker build -f release/Dockerfile --target basic -t chitu-basic:local .
docker build -f release/Dockerfile --target admin -t chitu-admin:local .
docker build -f release/Dockerfile --target rest -t chitu-rest:local .
```

При deployment задайте starter env из его README, монтируйте PHC/public key
read-only, используйте `--read-only --cap-drop=ALL
--security-opt=no-new-privileges` и TLS reverse proxy из production guide.
Миграции и seed Admin выполняются отдельными одноразовыми jobs; credentials не
встраиваются в image. Basic/REST data и Basic/Admin sessions process-local;
операционные ограничения описаны в starter README и не исчезают при packaging.

## Доказательства и проверка artifacts

- `source.tar`, `release.json`, `SHA256SUMS`: source commit, source archive digest,
  platform, recipe/scanner и digests каждой поставки. SHA256SUMS проверяйте до
  распаковки. Для проверки авторства нужен отдельно доверенный подписанный канал
  владельца; локальный checksum не является подписью.
- `basic-1.oci.tar`, `admin-1.oci.tar`, `rest-1.oci.tar`: OCI images с BuildKit
  SPDX 2.3 SBOM и provenance mode=max; predicates дополнительно извлечены в
  `*.spdx.json`/`*.provenance.json`. `release/verify.mjs` проверяет content hashes,
  размеры, platform/user, наличие обоих predicates и subject digest image.
- `frontend.spdx.json` дополняет Go build info npm graph, который scanner не
  может восстановить из minified CSS. Supplement создаётся из
  checksum-locked production npm closure, хранится внутри Basic/Admin image и
  извлекается из того же проверенного runtime. Pinned scanner включает его
  packages в итоговый image SBOM. Он перечисляет всю closure,
  включая пакеты demo entry, отсутствующие в minimal starter assets.
- Reproducibility gate сравнивает полный runtime manifest digest двух сборок:
  binary, assets, notices, CA, file metadata и image config. Аттестации содержат
  время выполнения и могут отличаться; равенство OCI tar/index с attestations
  не требуется. Проверена указанная platform; другие architectures не заявлены.
- `module-check`: временный Git repository с локальными version tags и private
  file GOPROXY. Официальный `golang.org/x/mod/zip` формирует module archives.
  Внешний consumer с новым module cache делает `go get` по version, импортирует
  root/три adapters, выполняет test/`go mod verify`, устанавливает зависимости
  Admin и собирает его. Нет replace на checkout. Только project namespace
  исключён из public sumdb; остальные модули проверяются штатно. Это проверка
  формата tagged distribution, а не установка опубликованного public tag.
- `smoke.json`: production launch всех трёх images с read-only rootfs, UID/GID
  65532, dropped capabilities и no-new-privileges; Basic login/assets, PostgreSQL
  Admin migrate/seed/login, REST external JWT CRUD и SIGTERM exit=0. Порты только
  localhost, SQL data временные, ресурсы имеют уникальные имена и удаляются.
  HTTP smoke вручную переносит Secure cookies; полноценный TLS/browser контракт
  проверяется отдельными E2E jobs. Это не эксплуатационная приёмка 13.2.

Локальные provenance unsigned, не удостоверяют личность builder и не объявляют
SLSA level. SPDX scanner дополняет, но не заменяет license audit и vulnerability
gates; frontend inventory сохраняет всю production dependency closure, включая
пакеты, не вошедшие в выбранный entry. CA notice берётся из pinned Go base image.
Precompiled AdminKit bytes закреплены в `release/frontend-audit.json`; изменение
без пересмотра audit, inline/remote assets и upstream notices блокирует сборку.

Первичные спецификации: [Go module archives/tags](https://go.dev/ref/mod#zip-files),
[BuildKit attestations](https://docs.docker.com/build/metadata/attestations/),
[SBOM](https://docs.docker.com/build/metadata/attestations/sbom/),
[SOURCE_DATE_EPOCH](https://docs.docker.com/build/ci/github-actions/reproducible-builds/).
