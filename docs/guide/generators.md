# Проверяемая генерация

Для первого запуска используйте [generator quickstart](generator-quickstart.md):
полный runnable source, offline schema, model/CRUD/Vue/DDL и compilation в новом
Go-модуле. Ни SQL drivers, ни сервер не нужны. Для schema из настоящей DB далее
используйте DatabaseInspector и CLI своего приложения, зарегистрировав driver.

generate:model, generate:crud и generate:migration принимают `-dry-run` (пути и
new/changed/unchanged) и `-diff` (whole-file unified diff). Обе опции ничего не пишут,
включая каталоги. Diff показывает результат для review, но не разрешает overwrite.
Обычная запись использует O_EXCL; существующие файлы, symlinks и дубли output
отклоняются, частично созданный набор откатывается. Не запускайте генерацию в
каталогах, которые параллельно изменяет недоверенный процесс.

Публичные GenerateModel/GenerateCRUD/GenerateMigrationSQL и migration.Plan позволяют
сначала построить результат в памяти; gii.Preview сравнивает его с диском.
Идентификаторы и SQL types валидируются; некорректный Go-код отклоняется go/format.
Инспектор baseline читает nullable, varchar length и decimal precision/scale через
information_schema. Primary/auto/version факты задаются явными flags/options.
Decimal/numeric генерируются как string для точности, nullable columns — pointers.
При обновлении ранее сгенерированных decimal float64 моделей нужен явный migration
прикладного JSON-контракта: новые decimal значения JSON являются строками.

[Приёмка](../../integration/generator_conformance_test.go) читает настоящие схемы
PostgreSQL/MySQL, создаёт чистый временный Go-модуль, компилирует model/CRUD и запускает
тесты JSON/input allowlist. Generated up/down SQL действительно создаёт/удаляет таблицу.
Проверяются read-only preview, no-overwrite и отсутствие частичного вывода.
