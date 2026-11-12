# Приёмка 11.1: Basic browser starter

Дата: 13.11.2026. Basic предоставляет browser login/dashboard/logout для одного
процесса и одного настроенного аккаунта. [Полная инструкция](../examples/basic/README.md)
содержит запуск из checkout, production config, build и ограничения хранения.

## Реализовано

- HTML forms с hidden CSRF и Post/Redirect/Get; вход и выход работают без JavaScript.
  Validation показывает ошибки на странице, сохраняет только username, не password.
  Dashboard перенаправляет гостя на login; logout — только POST.
- PasswordAuthenticator/Argon2id, атомарный rehash CAS, bounded account/client limiter,
  session/CSRF rotation, запрет старых ID, ограниченный session store с expiry cleanup.
  Предел sessions по умолчанию 5000; заполнение возвращает 503 без вытеснения активных
  sessions, rotation при заполненном store разрешена.
- Embedded Go views + AdminKit, desktop/mobile layout, отдельный Vite entry Basic.
  AssetBundle читает JS/CSS из manifest; fixed filenames удалены. Missing manifest/asset
  блокирует boot. Static handler использует os.Root, не публикует manifest/listings,
  не создаёт sessions на CSS/JS requests; hashed assets имеют immutable caching.
- `node scripts/basic.mjs start` выполняет npm ci, Vite build, frontend smoke test,
  Go build и запуск; `build` создаёт binary отдельно. Make и Docker E2E helpers добавлены.
- Production требует username, Argon2id PHC file и точный host authority allowlist,
  включает Secure/HttpOnly/SameSite=Lax `__Host-` cookie, production wrapper/logger,
  CSP/HSTS и finite HTTP/shutdown timeouts. Нет implicit demo password в production.
  Trusted proxy CIDR задаются явно. PHC utility читает password из stdin.
- CI job собирает из checkout, выполняет npm audit high gate, frontend tests/build,
  Chromium HTTP/production HTTPS E2E и публикует browser artifacts при success/failure.

## Проверки

| Проверка | Результат |
| --- | --- |
| Windows Go tests/vet всего модуля, повторная проверка изменённого Basic | Успешно |
| Linux vet + race всего ядра и examples | Успешно |
| Пять повторов Basic под Linux race | Успешно |
| Полный golangci-lint пакета Basic | 0 замечаний |
| Общий coverage ядра (examples исключены действующим script) | 84,6%, порог 80% |
| Автоматический build через Node helper на Windows | npm ci/build/test + Go build успешно |
| Docker multi-stage build из исходников | Frontend и binary собраны без checked-in public assets |
| Chromium E2E | 3/3: desktop + server validation, narrow/no-JS, production TLS + CSRF |
| npm audit | 0 уязвимостей, включая dev dependencies |
| actionlint, Node syntax, gofmt, diff --check, Markdown links | Успешно |

Последний полный browser run: 3/3 за 18,1s, включая HTTPS proxy и browser setup.
Проверены настоящие form submissions, пустые поля/неверный пароль, 303 redirects,
CSS/JS HTTP 200 и применение stylesheet, отсутствие JS/CSP errors, Secure cookie,
отказ cross-origin POST с 403, сохранение authenticated session после отказа и logout.
Скриншоты login/desktop/mobile просмотрены; narrow viewport не имеет горизонтального
переполнения. Docker artifacts сохранены в `.cache/basic-e2e/`.

Доказательства в исходниках: [HTTP/config tests](../examples/basic/main_test.go),
[session capacity/CAS/expiry](../examples/basic/sessions_test.go),
[frontend build smoke](../frontend/test/build.test.mjs),
[browser E2E](../frontend/e2e/basic.spec.js),
[test-only HTTPS harness](../scripts/basic-e2e-server.mjs),
[CI workflow](../.github/workflows/ci.yml).

## Границы и миграция

Default dev login — admin/changeme; production читает только явно настроенный PHC.
Прежний JSON login удалён: используйте HTML form с `_csrf`, после успешного POST —
navigation по Location. Go binary требует собранный public directory.

User/session/limiter state живёт в одном процессе. Restart завершает сессии; rehash
не записывается обратно в secret file. Persistent users и multi-node stores требуют
дальнейшей application configuration; Advanced Admin с persistence — этап 11.2.
TLS, сетевой доступ к backend и внешние request rate limits задаёт deployment.

Базовые SQL/cache/queue механизмы в этом шаге не изменены; real-service conformance
этапов 8–10 не расширялся. Прежний lint debt ядра и negative coverage-script gates
остаются в A4/12, как и прочие незакрытые release checks. Live remote CI results и
branch protection локальным Docker run не подтверждаются; production-ready v1.0
не объявляется.
