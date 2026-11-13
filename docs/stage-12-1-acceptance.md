# Этап 12.1 — CI и достоверный quality gate

Дата начала: 13.11.2026. Реализация и локальные результаты; hosted CI/required checks
не объявляются пройденными до run владельца после push. [Контракт](guide/quality-gates.md).

## Изменения

- OS/Go matrix minimum/latest patch ветки 1.26 на Ubuntu/Windows, четыре application
  modules, integration compile/vet и Linux race; per-matrix JSON artifacts.
- Полный lint вместо diff-only: 86 прежних findings исправлены. Read-only cleanup
  явно best-effort, CLI output errors передаются caller, удалён unused integer
  helper, SQL/asset formatting упрощён. Только intentional nil-context rejection
  tests имеют построчные SA1012 exemptions; правила для packages не отключены.
- Frontend ESLint/Vue essential, lint/build/test/audit и отдельные browser suites.
  `frontend/go.mod` ограждает framework tools от Go source внутри npm dependencies.
- Coverage gate fail-fast, validation thresholds/profile/total и preservation старого
  output; отдельная negative suite для всех subprocesses и ложного total.
- Security tools в checksum-locked отдельном Go module, govulncheck с tests,
  Gitleaks worktree/full history/redaction, npm severity/expiry policy без исключений.
- Govulncheck JSON exit semantics проверены: код 0 при findings. Обязательный gate
  переведён в text mode; найденная достижимая GO-2026-5004 устранена обновлением
  pgx 5.7.6 → 5.9.2 в Admin/integration. Старый JSON report не доказательство успеха.
- Actions SHA получены через upstream git refs; weekly Dependabot для Go/npm/
  Actions/Docker/Compose без auto-merge. Aggregate gate отвергает missing/skipped/
  cancelled/failure и проверен negative tests. Tag release validation вызывает CI.

Новые application regressions проверяют broken output в console help/migration
create: mutation могла завершиться, но ошибка записи теперь не скрывается как success.
Не следует слепо повторять schema mutation после потери output; сначала status.

## Воспроизведение

```bash
make vet test
make lint
make coverage-test coverage
make frontend security docs-test
sh scripts/integration.sh
sh scripts/basic-e2e.sh
sh scripts/admin-e2e.sh
go test -race -tags e2e ./examples/rest-api
```

На Windows core checks выполняются Go-командами; negative coverage wrapper можно
проверить Git sh. Integration/Basic имеют PowerShell helpers; process-only
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/integration.ps1`
не меняет system ExecutionPolicy. Linux race и browser/SQL acceptance используют
Docker Linux containers. Security требует сети для актуальной vulnerability DB.

## Доказанные результаты и остающаяся приёмка

Windows root/adapters/Admin vet/tests и integration compile/vet прошли, включая
повтор после pgx update. Frontend lint/build/test и npm audit прошли (0 advisories,
waivers отсутствуют). Negative gate/policy tests и coverage-script cases прошли.
Gitleaks: worktree и 48 локальных commits без находок. Govulncheck text/test на
Windows root/adapters/Admin прошёл; integration повторён после DNS failure.

Итоговый native golangci-lint v2.13.0 (собран Go 1.26.11) прошёл для root с e2e и
adapters/integration/Admin с integration tags: **0 замечаний во всех четырёх modules**.
Positive coverage gate на Windows/Git sh прошёл: **84,5%** при required 80%, negative
suite также прошла. Docs: 50 Markdown documents, 493 links и 3 source fences;
isolated onboarding/generated compilation и Windows REST process E2E прошли.
Actionlint проверил оба workflow до сбоя Docker; policy/gate Node tests прошли.

Docker Desktop во время Linux full suite вернул HTTP 500/EOF. Real-service suite
не смог завершить startup; cleanup также получил EOF. Это инфраструктурный failure,
а не passed/skip; до повторного успешного прогона Linux/service/browser gates
считаются непроверенными. Локальные artifacts находятся в `.cache/security`,
`.cache/quality-coverage.out`; remote reports появятся только после push.

Полная приёмка требует успешных итоговых Linux lint/race/service/browser runs,
hosted matrix с artifacts, `Required quality gate` и настройки required checks/
release permissions владельцем. Workflow `release-ready` блокируется зависимостью
quality, но публикация ещё не реализована (этап 13) и ручные действия владельца
не ограничиваются одним YAML.

12.2 остаётся для critical branch coverage, fuzz/scheduled smoke; negative
coverage-script часть A4 реализована уже в 12.1. Production-ready v1.0 не объявляется.
