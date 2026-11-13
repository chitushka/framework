# Cookbook

Рецепты выполняются из корня checkout, кроме явно указанного перехода в новый
Go-модуль. Go берётся из `go.mod`; Node/npm нужны только browser starters, Docker
Compose — только real-service/browser suites. Начните с [onboarding](../getting-started.md).

| Задача | Рецепт |
| --- | --- |
| Собрать и развернуть один узел за TLS proxy | [Deployment](deployment.md) |
| Выбрать проверку и воспроизвести отказ | [Testing](testing.md) |
| Настроить доверие proxy, auth и входные данные | [Security](security.md) |
| Применить схему и разобрать dirty state | [Migrations](migrations.md) |
| Запустить worker, выбрать broker и обработать replay | [Queues](queues.md) |

[Generator quickstart](../generator-quickstart.md) создаёт компилируемый model/CRUD
без SQL-сервера. [API migration guide](../api-migration.md) объединяет несовместимые
изменения pre-v1 и последовательность обновления приложения.

Fences с `file=` совпадают с полными исходниками, а не с непроверяемой копией.
`go run ./tools/checkdocs -run` проверяет ссылки и запускает onboarding в новом
временном Go-модуле. Shell-рецепты для deployment требуют ваших hosts/secrets/DSN;
инструмент не выполняет произвольные команды из Markdown. Точные границы gate —
в [testing recipe](testing.md#проверка-документации).
