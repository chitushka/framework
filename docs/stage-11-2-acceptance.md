# Приёмка 11.2: Advanced Admin

Дата: 13.11.2026. [Инструкция запуска](../examples/advanced-admin/README.md)
содержит build/migrate/seed, DSN, production config и границы одного процесса.

## Реализовано

- Отдельный Go module с реальными PostgreSQL/MySQL drivers; десять однооператорных
  миграций под framework migration lock и атомарный идемпотентный seed с явно заданными
  Argon2id PHC. Нет username-only входа, fallback password или автоматического seed.
- PasswordAuthenticator, persistent CredentialStore/CAS rehash, auth epoch и
  server-side RBAC. Старые sessions не оживают после disable → enable; смена пароля
  отзывает sessions. Permission revoke читается из SQL без локального cache.
- HTML CRUD пользователей/справочника/ролей, назначения и отзыв ролей, просмотр
  аудита, CSRF, PRG, validation и allowlist полей. Optimistic versions возвращают
  409 при конфликте. Списки ограничены, assets читаются из Vite manifest через os.Root.
- SQL guard lock сериализует authorization, mutation, last-admin policy и audit в
  одной транзакции между независимыми connections. Ошибка audit insert откатывает
  mutation. Нельзя делегировать отсутствующие у actor права или управлять более
  привилегированным аккаунтом. Встроенные роли защищены от изменения/удаления.
- Bounded session backend вынесен из Basic в общий internal/starter с прежними
  capacity/rotation/CAS/expiry проверками. MySQL no-op UpdateItem исправлен в
  framework RBAC: zero changed rows больше не означает отсутствие существующего item.
- Node/Make helpers, Docker integration/browser/runtime targets и отдельный CI job.
  SQL и Chromium проверки выполняются последовательно, artifacts публикуются и при
  ошибке; новый Go module явно включён в Windows/Linux vet/tests и lint.

## Доказательства

| Проверка | Результат |
| --- | --- |
| Windows root tests/vet и отдельный Admin module | Успешно |
| Linux root vet + race, текущий snapshot исходников | Успешно |
| Общий coverage ядра | 84,6%, порог 80% |
| Lint новых изменений ядра | 0 замечаний |
| Полный lint Admin, включая integration-tagged source | 0 замечаний |
| PostgreSQL 17 + MySQL 8.4, реальные drivers, race | Оба backend прошли |
| Frontend Vite build/smoke: main/basic/admin entries | Успешно |
| Chromium admin E2E | 4/4, около минуты вместе с запуском |
| Basic browser regression после общего session backend/Vite entry | 3/3: desktop, no-JS mobile, production HTTPS |
| Runtime image (scratch, USER 65532) | Собран |
| Node/shell syntax, Compose config, actionlint, Markdown links, gofmt | Успешно |

SQL suite проверяет migrations up/down/up, seed no-op и persistence через второй
connection pool, optimistic conflicts, CSRF/mass assignment и прямые HTTP запреты,
delegation ceiling/password-reset escalation, live revoke, auth epoch, last-admin
race двух pools, rollback при отказе audit и отсутствие secrets в audit details.
MySQL выполняет migrations с `multiStatements=false`; no-op metadata update проверен
также на настоящем сервере. Trigger для инъекции отказа требует test-only MySQL
`log_bin_trust_function_creators`; настройка находится только в acceptance Compose.

Browser suite проверяет guest, catalog CRUD editor, отсутствие недоступных UI links
и отказ прямых HTTP mutations, admin CRUD пользователей/ролей/назначений, аудит,
отзыв permissions в уже открытой сессии и делегированного RBAC manager. Настоящий
Go process перезапускается без replay seed: users, roles, assignments, catalog и
audit остаются, sessions завершаются. Скриншоты каталога и назначений просмотрены.

Финальный SQL/race run: PostgreSQL 10,4s, MySQL 24,0s, весь module 36,1s.
Дополнительный Basic run прошёл за 43,3s; из-за медленного локального Docker при
предыдущих попытках превышались startup/test budgets. В успешной локальной команде
использованы `docker run --init` и `npx playwright test --timeout=120000` из frontend;
application и CI timeouts не менялись. Полный lint ядра с прежним debt не объявляется
успешным: проверены новый Admin module целиком и diff изменений ядра.

Исходники: [SQL acceptance](../examples/advanced-admin/integration_test.go),
[config/templates](../examples/advanced-admin/config_test.go),
[browser tests](../frontend/e2e-admin/admin.spec.js),
[test-only restart harness](../scripts/admin-e2e-server.mjs),
[RBAC regression](../rbac/database_test.go),
[shared sessions](../internal/starter/sessions_test.go),
[workflow](../.github/workflows/ci.yml).

Повторение из checkout: `sh scripts/admin-e2e.sh` или `make admin-e2e`.
Helper создаёт отдельный случайный Compose project без host ports, использует
tmpfs test databases и удаляет volumes/containers. Локальный прогон выполнен
эквивалентными последовательными build/up/run integration/run browser командами
в частном project `chitu-admin-verify`. Артефакты находятся в `.cache/admin-e2e/`.

## Границы

Sessions и login limiter process-local; restart завершает sessions. Это starter
для одного узла с SQL persistence, а не готовый shared session/limiter deployment.
Общая guard row подходит небольшому административному workload. Audit защищён от
частичного application commit, но не от владельца DB; backup/retention/DB grants
настраивает deployment. TLS, внешние rate limits, MFA и recovery не поставляются.
Production config проверен unit tests; production HTTPS browser coverage остаётся
в Basic. Этот шаг не объявляет весь framework production-ready v1.0.

Накопленный lint debt ядра и negative coverage-script gates остаются в A4/12.
Удалённый CI и branch protection локальными проверками не подтверждаются.
Следующий шаг плана — 11.3, REST starter.
