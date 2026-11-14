# Этап 12.2 — критические границы и fuzzing

Дата: 14.11.2026. Локальная приёмка; hosted CI и scheduled run требуют push
владельца. [Quality contract](guide/quality-gates.md).

## Изменения

- Сохранён общий core coverage ≥80%. Тот же профиль дополнительно проверяется
  по statements: auth ≥85%, session ≥85%, security ≥90%, до округления.
  Missing/empty/corrupt/duplicate profile data дают failure; старый output остаётся
  нетронутым при failure любого gate. Negative tests проверяют независимость
  package thresholds и недостаточное покрытие при успешном общем total.
  Исправлен прежний false pass общего gate: 8000/10001 statements округляются
  Go до 80,0%, но теперь корректно отклоняются порогом 80% по исходным счётчикам.
- Восемь fuzz targets: router raw paths/URL round trip, bounded JSON binding,
  query value isolation/immutability, raw placeholders, RESP allocation bounds,
  JWT policy, PHC canonical encoding/cost bounds и cookie authentication.
  Два workers, ограниченные inputs; PHC parser не запускает Argon2.
- Обязательный CI smoke: 3 секунды на target; extended workflow: 5 минут на target
  еженедельно или вручную. Logs/crashing corpus сохраняются в artifacts.
  Корпусные seeds входят в обычные и Linux race tests. Runner дополнительно
  проверяет discovery точного target и не принимает Go success без fuzz tests;
  missing target/subprocess errors/fail-fast закреплены negative suite.
- [Session regression tests](../session/error_contract_test.go): commit failure блокирует Write/WriteHeader/Flush/Hijack
  и не повторяет persistence; middleware сохраняет обе ошибки handler/commit и
  восстанавливает writer; отменённые backend operations не меняют session state;
  [Ошибки session value/destroyed state/form parsing в CSRF](../security/error_contract_test.go) блокируют handler.
- JWT invalid-config tests теперь получают непустой key set: ранее все cases
  останавливались на empty-key-set check и не проверяли заявленные config branches.

## Воспроизведение

```bash
make coverage-test coverage
make fuzz
go vet ./...
go test ./...
go test -race -tags e2e ./...
make lint
go run ./tools/checkdocs -run
```

Coverage wrapper требует Go из go.mod и Node 22; shell команды на Windows
выполняются через Git sh. Для длительного fuzzing: `sh scripts/fuzz.sh 5m`.

## Результаты и ограничения

Windows core vet/tests прошли; финальный JWT regression повторён после review.
Linux root/adapters/Admin vet/race, REST process E2E и integration compile/vet
прошли. Полный golangci-lint v2.13.0 всех четырёх modules — **0 issues**.

Core profile: **84,97%** при минимуме 80%; auth **86,60%** (85%), session
**87,31%** (85%), security **94,73%** (90%). Значения показаны до сотых вниз;
сам gate сравнивает целочисленные счётчики точно. Positive gate, Node/shell
negative coverage cases и fuzz runner negative suite прошли. Финальный fuzz smoke
**8/8 targets по 3 секунды**, с проверкой discovery, прошёл без crashers.

Повторная приёмка 12.1: полный real-service baseline прошёл, включая generated
projects и process/crash tests. Basic browser **3/3**, Advanced Admin SQL/race на
обеих baseline СУБД и browser **4/4** прошли. Frontend lint/build/test/audit — без
advisories. Govulncheck text/test: **0 достижимых symbol findings** во всех modules;
есть module/package-only advisories без подтверждённого вызова vulnerable symbols,
они не подавляются. Gitleaks worktree/history — без находок. Actionlint прошёл.

Documentation gate проверил links/source fences и выполнил clean onboarding и
generated compilation. Локальные логи — `.cache/quality/12-2`; стартеры/scanners —
`.cache/quality-12-2-*.log` и `.cache/security`. Все scripts сохраняют ненулевой
status при failure; неуспешные предварительные запуски не считаются приёмкой.

Coverage — statement coverage, не полное branch coverage. Fuzzing проверяет
инварианты на исследованном корпусе и не доказывает отсутствие дефектов.
Пятиминутный scheduled job здесь описан как настроенный, а не выполненный.
12.1/A4 сохраняет отдельное требование hosted matrix/required checks владельцем;
v1.0 и эксплуатационная приёмка RC этим инкрементом не объявляются.
