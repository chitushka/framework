# Миграции перед переключением трафика

## Отдельный release job

Для рабочего SQL starter нужны отдельная PostgreSQL/MySQL база, DSN и права DDL.
Проверьте backup/restore до destructive изменений. Команда Admin из корня checkout:

```bash
node scripts/admin.mjs build
# Задайте ADMIN_DB_DRIVER и ADMIN_DB_DSN по Admin guide.
node scripts/admin.mjs migrate
```

`migrate` применяет schema под framework lock и не делает seed автоматически.
Первоначальный `seed` требует двух явных PHC-файлов; его нельзя подменять reset
паролей при каждом deploy. [Admin commands](../../../examples/advanced-admin/README.md)
описывают PostgreSQL/MySQL DSN и idempotent seed.

В своём приложении зарегистрируйте `console.RegisterTools` и `database/sql` driver
отдельным blank import, затем соберите собственный CLI. Framework core не
регистрирует SQL drivers за вас. Для такого binary (здесь он назван `app`):

```bash
app migration:status -driver pgx -dialect postgresql -dir migrations
app migration:up -driver pgx -dialect postgresql -dir migrations -timeout 5m
```

DSN передайте через `DB_DSN`/secret manager, не выводите его в logs. Все flags до
positional arguments. Generator quickstart начинает с детерминированной схемы без
сервера и создаёт up/down SQL для review: [полная инструкция](../generator-quickstart.md).
Для live database замените schema source на `ar.DatabaseInspector`, явно задав
primary/auto/version metadata. Сначала `-dry-run/-diff`, затем запись в новый путь.

## Dirty state

Lock сериализует runners, но не превращает MySQL DDL в транзакционный. При ошибке
или kill Runner сохраняет dirty marker и блокирует автоматический retry.
Не редактируйте checksum применённого файла и не повторяйте `up` вслепую.

1. Остановите release, прочитайте `migration:status` тем же CLI/DB/history config.
2. Сверьте marker с фактической схемой, данными и backup. Восстановите ожидаемое
   состояние вручную; выбор между forward fix и rollback делает оператор.
3. Только после проверки вызовите `migration:resolve` с точными ID/checksum и
   `-applied=true` для полностью выполненного up либо false для полностью
   отменённого изменения. Resolve меняет history/state, не исправляет SQL сам.
4. Снова проверьте status, затем продолжайте release. Для rolling update заранее
   применяйте expand/contract schema, совместимую с обеими application versions.

Lock/release budgets, nontransactional PostgreSQL flag, точные resolve flags и
process/crash evidence — в [migration contract](../migrations.md). Настоящие
PostgreSQL/MySQL scenarios выполняются `sh scripts/integration.sh`; offline
quickstart проверяет генерацию/компиляцию, но не выполнение DDL на сервере.
