# Quality gates и обновление зависимостей

Конфигурация: [CI](../../.github/workflows/ci.yml),
[release validation](../../.github/workflows/release-check.yml),
[Dependabot](../../.github/dependabot.yml). Нет path filters, continue-on-error или
разрешения пропускать обязательную проверку. Push/PR, workflow_dispatch и reusable
workflow используют один набор jobs.

## Обязательные проверки

| Job | Проверка и локальный эквивалент |
| --- | --- |
| `test` | OS/Go matrix: Ubuntu 24.04, Windows 2025; minimum 1.26.11 и latest patch 1.26.x. Root/adapters/Admin vet/tests, Linux race, integration compile/vet. Windows также REST process E2E и isolated onboarding |
| `coverage` | `make coverage-test coverage`: общий core ≥80%, отдельно auth/session ≥85%, security ≥90%; Go + Node 22 |
| `fuzz` | `sh scripts/fuzz.sh 3s`: восемь targets с двумя workers, обязательный smoke на каждом CI run |
| `lint` | golangci-lint v2.13.0 во всех четырёх application modules, включая e2e/integration tags; `make lint` |
| `frontend` | Node 22, `npm ci`, `npm run lint`, build/test, `node --test scripts/*.test.mjs`, `node scripts/npm-audit.mjs` |
| `security` | `sh scripts/security.sh`: checksum-locked tools, govulncheck с tests, Gitleaks worktree/full history |
| `integration` | `sh scripts/integration.sh` или `./scripts/integration.ps1`: реальные PostgreSQL/MySQL/Redis/Memcached/RabbitMQ/OTel, process recovery и generated project compilation |
| `docs` | `go run ./tools/checkdocs -run`, checker negative/race tests |
| `rest` | `go test -race -tags e2e -count=1 ./examples/rest-api` |
| `basic` | `sh scripts/basic-e2e.sh`, production HTTPS/CSRF/browser flows |
| `advanced-admin` | `sh scripts/admin-e2e.sh`, SQL/race и browser/restart/escalation |

`quality-gate` имеет `if: always()` и зависит от каждого job выше. Его script
принимает только `success`: missing/failure/cancelled/skipped дают ошибку.
[Regression tests](../../scripts/require-success.test.mjs) проверяют эти состояния,
полноту needs относительно jobs и full SHA для каждого внешнего action.

В настройках репозитория владелец должен сделать **Required quality gate**
обязательным status check, запретить обход branch protection и разрешить публикацию
только через защищённый release process. `Release validation` на `v*` tags вызывает
тот же reusable CI; `release-ready` не запускается при его провале. Этот workflow
не публикует release: упаковка/подпись/публикация относятся к этапу 13 и отдельному
разрешённому действию. YAML не запрещает владельцу ручной release вне workflow.

Проверки и artifacts конкретного GitHub run появляются после push владельцем.
Локальный эквивалент не доказывает настройки hosted runners/required checks.
Точное состояние приёмки — в [отчёте 12.1](../stage-12-1-acceptance.md).

## Coverage без ложного успеха

Coverage wrapper проверяет threshold как конечное число 0..100, непустой список
core packages и профиль с mode/statements. `go list`, `go test`, `go tool cover`
запускаются без pipeline, способного скрыть их ненулевой status. Total должен быть
единственным числом 0..100%; пустой/некорректный/недостаточный профиль отклоняется.
Старый caller profile заменяется только после успешного gate. Negative suite
проверяет все subprocess failures, empty/header-only/zero-statement profiles,
неверные totals/thresholds, below-threshold и сохранение прежнего файла.

[Critical coverage gate](../../scripts/check-critical-coverage.mjs) независимо
суммирует statements из того же профиля для `auth` (85%), `session` (85%) и
`security` (90%). Пакет нельзя скрыть высоким покрытием остальных packages или
удалить из профиля: missing/zero-statement packages, malformed/duplicate blocks
отклоняются. Порог сравнивается до округления; 84,99% не проходит 85%.
Общий threshold также перепроверяется по исходным счётчикам: 79,992% не проходит
80%, даже если `go tool cover` округлил total до 80,0%. Decimal thresholds
сравниваются точно, без преобразования к float.
[Negative tests](../../scripts/check-critical-coverage.test.mjs) проверяют эти
случаи, shell suite также проверяет сохранение старого файла при critical failure.
Node 22 требуется для coverage wrapper и проверяется в CI отдельным setup step.

Go coverage измеряет statements, а не все логические ветви. Ошибки CSRF/session
commit и JWT configuration дополнительно закреплены regression tests; это не
доказательство исчерпывающего покрытия всех error paths. Текущие результаты и
границы проверки — в [отчёте 12.2](../stage-12-2-acceptance.md).

`frontend/go.mod` — граница без Go packages: npm dependencies могут содержать Go
source, который не должен попадать в root `./...`, lint или coverage. Library code
и coverage threshold не исключаются ради зелёного результата.

## Fuzzing критических парсеров

`make fuzz` запускает тот же [runner](../../scripts/fuzz.sh), что обязательный
CI job `fuzz`: каждый target выполняется отдельно, ошибка Go или найденный crasher
останавливает runner с ненулевым кодом. Перед прогоном проверяется точное имя
через `go test -list`: отсутствие target тоже блокирует gate, даже если Go вернул
успех без fuzz tests. [Negative runner suite](../../scripts/test-fuzz.sh) проверяет
missing target, ошибки discovery/fuzz subprocess и остановку после первой ошибки.
Проверяются raw/encoded router paths и
обратная генерация URL, JSON binding с обычными/chunked bodies, SQL identifiers и
отдельные bound values, raw placeholders, RESP lengths/delimiters, JWT verification,
PHC costs/canonical encoding и encrypted session cookies. Данные и workers
ограничены; PHC fuzzing не выполняет дорогостоящий Argon2.

[Extended fuzzing](../../.github/workflows/fuzz.yml) запускается по воскресеньям
в 02:23 UTC или через workflow_dispatch: по 5 минут на каждый из восьми targets,
общий timeout 60 минут. Локальный эквивалент: `sh scripts/fuzz.sh 5m`.
Оба workflow сохраняют `.cache/fuzz/*.log` и найденные `testdata/fuzz` inputs,
в том числе при failure. Без `-fuzz` все corpus seeds исполняются обычным
`go test ./...` и Linux race matrix. Scheduled run не заменяет обязательный smoke
и не входит в PR gate, поскольку он запускается независимо от PR.

При находке воспроизведите конкретный input командой
`go test ./web -run 'FuzzRouterURLRoundTrip/<hash>'` (подставьте package/target/hash
из отчёта). Сохраните минимальный input в `testdata/fuzz/<target>` или добавьте
именной regression test; после исправления выполните обычные/race tests и fuzz
target. Не удаляйте input и не ослабляйте инвариант только ради прохождения gate.

## Vulnerabilities и исключения

Tools зафиксированы в [tools/security/go.mod](../../tools/security/go.mod) и go.sum:
govulncheck v1.9.0, Gitleaks v8.29.0 (канонический Go module path
`github.com/zricethezav/gitleaks/v8`). Checksum verification включена; при несовпадении
проверка останавливается. Tools не добавляют dependencies в runtime framework.

Govulncheck используется с `-format=text -test`: JSON/SARIF/OpenVEX возвращают 0
даже при находках и не могут служить exit-code gate. Проверяются root/adapters/
integration/Admin, в том числе drivers из test-only module. Ошибка сети/базы/анализа
тоже блокирует gate. Reachable findings не подавляются; module-only сообщения
не приравниваются к доказанной достижимости. В 12.1 pgx Admin/integration обновлён
до 5.9.2 после находки GO-2026-5004. Повторный scan обязателен на целевом OS/Go.
Формат и ограничения — в [официальной документации govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck).

Npm audit проверяет production и dev dependencies; network/error/malformed reports
не превращаются в пустой список. По умолчанию блокируется любая severity. Только
low/moderate advisory может иметь явное исключение в
[security-exceptions.json](../../.github/security-exceptions.json): точные package/GHSA,
owner, содержательная reason, created/expires и не более 14 дней. Expired, duplicate,
unused exception и попытка waiver high/critical отклоняются. Сейчас список пуст.
[Negative tests](../../scripts/npm-audit.test.mjs) проверяют эту политику.

ESLint/Vue plugin выполняют JS/Vue essential rules с `--max-warnings 0`. Версии
tools закреплены lockfile; выбран ESLint 9.39.5 без advisory minimatch из проверенной
ветки 10. Новые major updates рассматриваются отдельным PR с audit и browser suites,
а не через `npm audit fix --force` или автоматическое принятие исключений.

Gitleaks сканирует worktree и всю доступную Git history, с redaction, без inline
`gitleaks:allow`; исключены только dependency/build directories. Нет baseline
fingerprints для application source. CI checkout использует fetch-depth 0 для
history scan и persist-credentials false. Не загружайте неотредактированные secrets
в artifacts; reports security gate уже редактируются scanner.

## Контролируемые обновления

Все внешние Actions закреплены 40-символьными upstream commit SHA, linter — точной
версией. Dependabot еженедельно создаёт PR для Actions, всех application/tool Go
modules, npm, Dockerfiles и Compose images; minor/patch updates Go/npm группируются,
major остаются отдельными. Автоматическое merge не включено.

Перед merge проверьте diff manifest/lock/checksums/digests и provenance upstream,
согласуйте общие Go dependencies между modules, выполните все обязательные jobs,
обновите support/migration guide при изменении API или baseline. SQL/cache/queue
updates требуют real-service suite, frontend — browser E2E. Secrets и security
waivers нельзя добавлять как побочный эффект dependency PR. Основания:
[GitHub SHA pinning](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/find-and-customize-actions),
[Dependabot options](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference).
