# SQL baseline contract

Поддерживаемые СУБД для conformance — PostgreSQL 17/pgx и MySQL 8.4/go-sql-driver/mysql.
Oracle и SQL Server имеют только dialect/unit tests и не входят в этот контракт.
[Единый suite](../../integration/sql_conformance_test.go) проверяет quoting reserved
identifiers, параметры с кавычками/Unicode, NULL, timestamp с микросекундами, decimal
без преобразования через float, affected rows, pool exhaustion, отмену активного SQL,
savepoint/outer rollback, commit/rollback при смерти соединения и transactional DDL.
Generated IDs проверяются вместе с [AR conformance](../../integration/ar_conformance_test.go).

Decimal храните строкой, sql.Scanner/driver.Valuer либо прикладным decimal type:
float64 не сохраняет точную десятичную арифметику. TIMESTAMP(6) сохраняет микросекунды;
используйте UTC и parseTime=true в MySQL DSN. NULL требует pointer/sql.Null*.
PostgreSQL генерирует IDs через RETURNING; MySQL — LastInsertId.

RowsAffected в MySQL по умолчанию считает изменённые строки, PostgreSQL — совпавшие.
Для no-op UPDATE без version используйте clientFoundRows=true в MySQL DSN, если
приложению нужна одинаковая семантика совпадения. Versioned AR всегда изменяет version.
MySQL DDL имеет implicit commit; PostgreSQL обычный DDL откатывается транзакцией.
CREATE INDEX CONCURRENTLY требует nontransactional migration.

После ошибки commit результат может быть неопределённым при разрыве сети: не
повторяйте неидемпотентную операцию вслепую. Failed transaction не используется повторно.
Задавайте контекст всей транзакции и SQL: database/sql не предоставляет CommitContext.
Pool wait входит в deadline; отменённый/повреждённый connection драйвер может удалить
из pool. Cleanup получает отдельный ограниченный context.
