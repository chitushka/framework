# Advanced Admin

Browser starter с постоянными пользователями, RBAC, справочником и аудитом в
PostgreSQL 17 или MySQL 8.4. HTML forms работают без JavaScript; AdminKit загружается
через Vite manifest. Это отдельный Go module: SQL drivers не добавляются в ядро.

## Запуск из checkout

Нужны Go из go.mod, Node/npm из frontend/package.json и отдельная созданная база.
Все команды выполняются из корня репозитория. Сначала соберите binary/assets:

```powershell
node scripts/admin.mjs build
$env:ADMIN_DB_DRIVER = "pgx"
$env:ADMIN_DB_DSN = "postgres://USER:PASSWORD@localhost:5432/ADMIN_DB?sslmode=verify-full"
node scripts/admin.mjs migrate
```

Для MySQL задайте `ADMIN_DB_DRIVER=mysql` и DSN вида
`USER:PASSWORD@tcp(localhost:3306)/ADMIN_DB?parseTime=true&timeout=5s&readTimeout=10s&writeTimeout=10s&tls=true`.
TLS/certificates и доступ к базе настраивает владелец deployment. Миграции выполняются
отдельной командой под framework migration lock, по одному statement; MySQL
`multiStatements` не требуется. Сервер откажется стартовать с неполной схемой.

Создайте два разных пароля длиной 12–1024 байта. Utility читает один пароль из stdin
и выводит Argon2id PHC; направьте вывод в защищённые UTF-8/ASCII файлы вне checkout.
Не передавайте пароль аргументом командной строки и не сохраняйте его в истории shell.

```powershell
# Подайте пароль через stdin, например из вашего secret manager:
# <password source> | .cache/admin/admin.exe -hash-password
$env:ADMIN_SEED_ADMIN_HASH_FILE = "C:/secrets/admin.phc"
$env:ADMIN_SEED_EDITOR_HASH_FILE = "C:/secrets/editor.phc"
node scripts/admin.mjs seed
node scripts/admin.mjs start
```

Откройте `http://127.0.0.1:8081`. Войдите как `admin` или `editor` с заданными
паролями. Seed создаёт administrator/editor, два аккаунта и исходную запись
справочника одной транзакцией. Повторный seed не сбрасывает данные, пароли и права;
после успешного seed переменные/файлы hash не нужны серверу. Нет встроенного пароля,
username-only входа и автоматического seed при boot. `start` повторяет frontend/Go
build; уже собранный `.cache/admin/admin.exe` можно запускать напрямую из корня.

## Права и изменения

`administrator` имеет `catalog.view`, `catalog.manage`, `users.manage`, `rbac.manage`,
`audit.view`. `editor` читает и изменяет справочник. Меню и формы учитывают права,
а каждый запрос и mutation повторно проверяет текущий SQL RBAC на сервере.
Страницы: `/admin/catalog`, `/admin/users`, `/admin/roles`, `/admin/assignments`,
`/admin/audit`. Произвольные роли содержат плоский набор перечисленных permissions;
редактирование/удаление двух встроенных ролей запрещено.

Владелец `rbac.manage` может делегировать только собственные effective permissions.
Управление пользователем требует всех его permissions: это запрещает, например,
ограниченному менеджеру сбросить пароль администратора. Последнего активного
administrator нельзя отключить, удалить или лишить роли даже двумя конкурентными
SQL connections. Эти application policies не являются общим контрактом любого
использования framework RBAC.

Проверка actor, права, mutation и audit insert выполняются в одной транзакции под
общей SQL row lock. Ошибка аудита откатывает изменение. Audit хранит actor/action/
target/details/time без пароля или PHC; UI только читает его. Изменения имеют
optimistic version: устаревшая форма получает 409. Неизвестные поля отклоняются,
POST требует CSRF. Сброс пароля или изменение active увеличивает auth epoch и
немедленно инвалидирует старые sessions, включая disable → enable.

Списки пользователей/справочника/аудита ограничены 25 записями на страницу;
доступно до 200 ролей и 20 назначений на аккаунт. Сериализация SQL операций
предназначена для небольшого административного workload. HTTP request context
ограничен 10 секундами, пул — 8 connections, body/form — 16 KiB.

## Production

```powershell
$env:APP_ENV = "production"
$env:ADMIN_ALLOWED_HOSTS = "admin.example.com"
$env:ADMIN_TRUSTED_PROXIES = "10.0.0.0/24"
$env:ADMIN_ADDRESS = "0.0.0.0:8081"
.cache/admin/admin.exe
```

Прокси должен завершать TLS и корректно передавать Host/X-Forwarded-*; доверяйте
только своим CIDR. Allowlist содержит точный внешний authority, включая нестандартный
порт. Backend не публикуют напрямую в интернет. Включены production redaction,
CSP/HSTS и Secure/HttpOnly/SameSite=Lax `__Host-admin_session`.
`ADMIN_PUBLIC_DIR` по умолчанию `public`; отсутствующий manifest/asset блокирует boot.

Users/RBAC/catalog/audit переживают restart. **Sessions и login limiter находятся
в памяти одного процесса**: до 5000 sessions, idle TTL 30 минут, absolute TTL 8 часов,
10 попыток/account и 60/client в минуту. Restart завершает sessions и сбрасывает
limiter. Для нескольких узлов нужны общий AtomicBackend и общий AttemptLimiter;
этот starter их не поставляет. MFA, password recovery и управление schema rollback
через HTTP не реализованы. Резервное копирование, DB grants/retention, TLS и общие
rate limits остаются ответственностью deployment.

## Проверка

```bash
go -C examples/advanced-admin test ./...
go -C examples/advanced-admin vet ./...
sh scripts/admin-e2e.sh
```

Последняя команда собирает из checkout, запускает изолированные PostgreSQL/MySQL,
обязательные race integration tests и Chromium E2E последовательно, затем удаляет
контейнеры/volumes. Host ports не открываются. Пароли стенда используются только
для тестовых tmpfs баз. MySQL trigger setting включён только в acceptance Compose
для инъекции отказа аудита. Browser reports/screenshots остаются в `.cache/admin-e2e/`.
Отсутствующий DSN у tagged integration test приводит к fail, silent skip нет.
[Отчёт приёмки](../../docs/stage-11-2-acceptance.md).
