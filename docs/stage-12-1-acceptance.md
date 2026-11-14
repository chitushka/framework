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

13.11 Docker Desktop во время Linux full suite вернул HTTP 500/EOF; тот run не
принимался. **14.11 Docker Desktop запущен, локальная приёмка повторена успешно**:
Linux root/adapters/Admin vet/race, REST process E2E, integration compile/vet и
полный lint четырёх modules (0 issues); полный real-service baseline с SQL/AR/
generator/process/crash/queue/OTel tests; Basic Chromium 3/3 и Admin SQL/race плюс
Chromium 4/4. Frontend lint/build/test/audit прошёл, advisories/waivers — 0.
Windows core vet/tests прошли, включая повтор вне sandbox для doccheck TempDir.
Govulncheck text/test во всех четырёх modules: 0 достижимых symbol findings;
module/package-only advisories не приравниваются к достижимым и не подавлены.
Gitleaks worktree/history — без находок. Actionlint проверил все три workflow.

В [12.2](stage-12-2-acceptance.md) усилены coverage/fuzz gates: core profile
**84,97%**, auth **86,60%**, session **87,31%**, security **94,73%**;
общий и package thresholds проверяются до округления. Логи повторной Linux
приёмки — `.cache/quality/12-2`, starter/security logs — `.cache/quality-12-2-*.log`,
scanner reports — `.cache/security`. Remote reports появятся только после push.

Локальные Linux lint/race/service/browser runs завершены. Полная приёмка всё ещё
требует hosted matrix с artifacts, `Required quality gate` и настройки required checks/
release permissions владельцем. Workflow `release-ready` блокируется зависимостью
quality, но публикация ещё не реализована (этап 13) и ручные действия владельца
не ограничиваются одним YAML.

12.2 реализован и проверен локально; scheduled workflow настроен, его hosted run
здесь не объявляется выполненным. 12.1/A4 остаётся открытым до hosted приёмки и
настройки required checks владельцем. Production-ready v1.0 не объявляется.
