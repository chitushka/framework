# v1.0.0 — локально проверенный draft

Дата: 19.11.2026. Source: `740a769f7b039fb04dc5fecf95bde2e8e5207dc7`.
Platform: `linux/amd64`. Status: `unpublished-local-draft`.

Сохранены неизменённые [release manifest](release.json),
[SHA256SUMS](SHA256SUMS), [production smoke](smoke.json),
[independent verification](verification.json) и [полный лог prepare](prepare.log.gz).
`prepareLogSHA256` в verification считается по распакованному UTF-8 логу.
Checksums относятся к полным artifacts из локального каталога, включая source
archive, OCI и SPDX/provenance; большие archives здесь не дублируются.

Две серии сборок дали одинаковые runtime manifest digests Basic/Admin/REST.
Независимая инспекция обеих серий проверила blob hashes/sizes, SPDX/provenance,
subject identity и non-root/source/version config; source archive сверен с Git.
Внешний consumer установил root/adapters/Admin как v1.0.0 через private file
GOPROXY, выполнил compatibility fixtures, dependency verification и сборку Admin.
Smoke проверил production Basic/Admin/REST и SIGTERM exit=0.

Полные artifacts: `.cache/releases/v1.0.0-UsT3jq`; успешный путь также записан в
`.cache/releases/latest.txt`. Исходный принятый RC и его operational artifacts
сохраняются отдельно. Коммит этого evidence не меняет source identity draft.

Локальные provenance unsigned. Это проверка локальной поставки; публикация,
hosted CI/required checks и подпись владельца ещё не выполнены.
[Приёмка и ограничения](../../../stage-13-3-acceptance.md),
[формат artifacts](../../../guide/releases.md).
