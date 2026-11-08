# Active Record: целостность и границы транзакций

Versioned Update проверяет primary key и старую version одним UPDATE. Только
успешная запись изменяет version модели; конфликт возвращает ErrStaleModel,
переполнение типа — ErrVersionOverflow до SQL. Каждому конкурентному writer нужна
собственная копия модели: Go-структуры сами по себе не concurrency-safe.

Опциональный `ar.Validator` (`Validate(context.Context) error`) вызывается до
Insert/Update. Он не заменяет SQL constraints: unique проверяется базой. Для сценариев
нескольких записей приложение открывает `Connection.Transaction` и получает
`repository.WithExecutor(tx)`. После rollback отбросьте/перечитайте изменённые
объекты: присвоенные ID/version не являются подтверждением commit внешней транзакции.

FindByPK принимает части composite key в порядке db tags модели. Finder immutable;
pagination задаёт явный стабильный OrderBy (при равенстве добавьте primary key), Limit
и Offset. Глубокая offset pagination не является keyset pagination.

HasOne/HasMany выполняют явный отдельный запрос. LoadHasOne/LoadHasMany загружают
связь одним IN query для всей переданной пачки, результат выровнен по parents.
Ключи сопоставляются по driver value, включая pointers и разные целочисленные Go types.
Разбивайте большие списки на bounded batches с учётом лимита placeholders сервера;
relations не выполняются неявно при чтении поля.

[Conformance](../../integration/ar_conformance_test.go) на PostgreSQL/MySQL проверяет
generated IDs, двух конкурентных writers (один success, один stale), validation,
outer rollback, composite keys, pagination и один query для eager loading.
[Unit regressions](../../db/ar/hardening_test.go) проверяют overflow и SQL-equivalent keys.
Различия SQL backend — [SQL contract](sql-contract.md).
