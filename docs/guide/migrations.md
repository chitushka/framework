# Миграции и восстановление

Runner поддерживает baseline PostgreSQL/MySQL. PostgreSQL session advisory lock
сериализует все framework histories в одной database, MySQL GET_LOCK — в одной
schema, включая квалифицированные aliases. Lock берётся до чтения history и DDL.
Вся операция использует одно закреплённое sql.Conn; pool с одним соединением допустим.
Oracle/SQLServer Runner отклоняет: для них нет принятого lock/recovery contract.

Options.LockTimeout (30s) ограничивает получение соединения и lock, ReleaseTimeout
(5s) — независимый cleanup после отмены. При неопределённом результате acquire/unlock
физическое соединение удаляется из pool. При смерти процесса lock освобождает сервер.
Контекст вызова задаёт общий бюджет; CLI имеет -timeout=5m, -lock-timeout=30s,
-release-timeout=5s. Передавайте positive durations. History: [schema.]table,
ASCII table name до 57 байт; рядом создаётся table_state. MySQL требует InnoDB.

Перед SQL сохраняется durable dirty marker: ID, checksum, направление и started_at.
PostgreSQL по умолчанию выполняет SQL, history и удаление marker в одной транзакции.
MySQL DDL выполняется вне неё, поскольку имеет implicit commit. Для PostgreSQL
CREATE INDEX CONCURRENTLY укажите `-- chitu:nontransactional` первой строкой up/down
файла либо Migration.NonTransactional. Флаг входит в checksum. Произвольные SQL
транзакционные команды внутри migration не поддерживаются: границей владеет Runner.

Ошибка, отмена или падение оставляют dirty marker, даже когда PostgreSQL успел
откатить SQL. Автоматический retry запрещён через ErrDirty. Status показывает dirty,
checksum, направление и время; missing/modified source также блокирует up/down.

Порядок оператора:

1. Остановить release job и получить `migration:status` с теми же DB/table flags.
2. Проверить фактическую схему, данные и журналы. Восстановить согласованное состояние
   вручную, используя backup при необходимости. Не редактировать применённые файлы.
3. Выполнить `migration:resolve -id=ID -checksum=EXACT_CHECKSUM -applied=true|false`.
   Явное true означает, что up полностью выполнен; false — полностью отменён.
   Команда изменяет только history/state в транзакции, не выполняет SQL миграции.
4. Проверить status и повторить release. Неверный checksum или отсутствие dirty
   отклоняются; resolve не является автоматическим исправлением схемы.

Проверки: [unit recovery](../../db/migration/recovery_test.go),
[независимые процессы, kill и nontransactional DDL](../../integration/migration_process_test.go).
Тестовый tmpfs проверяет падение процесса, но не fsync/disk durability при потере host.
