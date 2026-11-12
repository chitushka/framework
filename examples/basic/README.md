# Basic starter

Один процесс, один настроенный пользователь, server-rendered HTML и AdminKit.
Вход и выход выполняются обычными HTML-формами, работают без JavaScript и проверяют
session-bound CSRF. После успешного POST сервер отвечает 303; пароль никогда не
возвращается в форму. Dashboard требует входа. Sessions и account/client limiter
работают в памяти процесса и имеют конечные limits.

## Запуск из чистого checkout

Нужны Go из `go.mod`, Node.js 22.12+ и npm. Все команды — из корня репозитория.

```bash
node scripts/basic.mjs start
```

Команда сама выполняет `npm ci`, Vite build, frontend smoke tests, сборку Go binary
и запуск сервера. Откройте **http://localhost:8080**, введите **admin / changeme**,
перейдите на dashboard и нажмите «Выйти». Это defaults только для development.

Для отдельной сборки:

```bash
node scripts/basic.mjs build
# Linux/macOS
.cache/basic/basic
# Windows PowerShell
.\.cache\basic\basic.exe
```

После сборки также работает `go run ./examples/basic`. Если manifest или один из
его ресурсов отсутствует, приложение отказывается запускаться с инструкцией
сборки. Go templates встроены в binary; рядом нужен каталог `public`.
Vite собирает entry `src/basic.js` вместе с существующим Vue scaffold `src/main.js`.
AssetBundle читает хэшированные CSS/module scripts из `.vite/manifest.json`.
Не копируйте имена ресурсов в Go/HTML и не публикуйте сам manifest как HTTP endpoint.

## Production configuration

Production включается явно через `APP_ENV=production`. В этом режиме нет fallback
пароля: обязательны username, файл Argon2id PHC и точный host allowlist. Cookies
имеют имя `__Host-basic_session`, Secure, HttpOnly, SameSite=Lax, Path=/, без Domain.
Приложение предназначено для работы за TLS reverse proxy с закрытым backend listener.

| Переменная | Значение / default |
| --- | --- |
| `APP_ENV` | `development` или `production`; default development |
| `BASIC_ADDRESS` | `127.0.0.1:8080`; numeric port 1–65535 |
| `BASIC_PUBLIC_DIR` | `public`, путь относительно рабочего каталога |
| `BASIC_USERNAME` | Development default `admin`; в production обязателен |
| `BASIC_PASSWORD_HASH_FILE` | UTF-8/ASCII файл с одним Argon2id PHC; production required |
| `BASIC_ALLOWED_HOSTS` | CSV **authorities**, включая порт, если он есть в Host; production required |
| `BASIC_TRUSTED_PROXIES` | CSV CIDR непосредственных proxies; default пусто, forwarding headers игнорируются |
| `BASIC_MAX_SESSIONS` | 5000; диапазон 1–100000 |

Development allowlist по умолчанию разрешает `localhost`, `127.0.0.1` и `[::1]`
с портом из BASIC_ADDRESS. Для нестандартного hostname задайте allowlist явно.
Например, `basic.example.com` для обычного HTTPS и `basic.example.com:8443` для 8443.
Proxy обязан перезаписывать `X-Forwarded-For`, `X-Forwarded-Proto` и `X-Forwarded-Host`;
указанные CIDR должны описывать доверенные proxies, доступ к backend ограничивается сетью.

Получите PHC через utility, читающую пароль **из stdin**, а не из argv/env.
Ниже Bash-пример; путь `/secure` должен принадлежать владельцу deployment.

```bash
umask 077
read -r -s -p 'Password (at least 12 bytes): ' password
printf '%s' "$password" | .cache/basic/basic -hash-password > /secure/basic-password.phc
unset password

APP_ENV=production \
BASIC_USERNAME=owner \
BASIC_PASSWORD_HASH_FILE=/secure/basic-password.phc \
BASIC_ALLOWED_HOSTS=basic.example.com \
BASIC_TRUSTED_PROXIES=127.0.0.1/32 \
.cache/basic/basic
```

На Windows utility `.cache/basic/basic.exe -hash-password` также читает stdin;
передайте его из доверенного secret provider, сохраните PHC как ASCII/UTF-8 без BOM,
затем задайте те же `$env:...` переменные. Файл PHC хранится вне `public` и checkout.

Production wrapper отключает diagnostics, скрывает внутренние ошибки, logger
редактирует secrets, HTTP timeouts конечны. CSP разрешает локальные CSS/JS,
запрещает frames, inline scripts и внешние form actions; включён HSTS.
Формы ограничены 8 KiB и восемью values, лишние/дублированные поля отклоняются.
Логин ограничен пятью попытками на account и 30 на client за минуту, включая успех.
Sessions имеют idle TTL 30m и absolute TTL 8h. Expired IDs удаляются даже без повторного
визита браузера. При заполнении store новые sessions получают 503, существующие
продолжают работать; rotation не требует дополнительного свободного slot.

Это стартовая конфигурация **одного** процесса: restart завершает все сессии,
users/limiter не разделяются между nodes, rehash CAS обновляет hash только в памяти.
Для постоянных пользователей/нескольких nodes замените `accountStore`, session
AtomicBackend и AttemptLimiter совместно; секреты конфигурации обновляются при restart.
Persistent users/admin UI относятся к 11.2. Наличие starter не объявляет весь
framework production-ready v1.0. TLS, external request-rate limits и deployment
задаются владельцем приложения; [proxy contract](../../docs/guide/proxy.md) и
[lifecycle contract](../../docs/guide/lifecycle.md) описывают границы.

## Проверка

```bash
go test ./examples/basic
go vet ./examples/basic
node scripts/basic.mjs build

# Chromium + Node + Go из закреплённых Docker images, без локального browser install
docker build -f examples/basic/Dockerfile.e2e -t chitu-basic-e2e .
docker run --rm chitu-basic-e2e
```

Также доступны `make basic`, `make basic-build`, `make basic-e2e`,
`sh scripts/basic-e2e.sh` и `scripts/basic-e2e.ps1` (если policy разрешает scripts).
Изменять системную PowerShell policy для двух Docker-команд не требуется.

При локально установленном OpenSSL и Chromium для Playwright:

```bash
cd frontend
npx playwright install chromium
cd ..
node scripts/basic.mjs test
```

E2E helper использует временные test-only certificate/hash, запускает development
на 8181 и production на 8182 за локальным HTTPS proxy на 8443. Эти порты должны быть
свободны. Тесты проверяют server validation/invalid credentials, manifest assets,
login/dashboard/logout, narrow viewport без JavaScript, production Secure cookies,
отсутствие demo fallback и cross-origin POST без CSRF. Test keys удаляются при teardown.
Browser artifacts находятся в `frontend/test-results` и `frontend/playwright-report`.
Docker scripts сохраняют их в `.cache/basic-e2e/`; CI job **Basic frontend build and
browser E2E** публикует этот каталог как artifact даже при ошибке теста.

Прежний JSON login удалён: browser clients используют `application/x-www-form-urlencoded`
и hidden `_csrf`; clients с валидным CSRF header и JSON body получают 415.
