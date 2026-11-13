# Deployment одного узла

## Сборка и конфигурация

Сначала выберите модель хранения: [Basic](../../../examples/basic/README.md) —
browser starter без постоянных пользователей, [Admin](../../../examples/advanced-admin/README.md) —
SQL users/RBAC/catalog/audit, [REST](../../../examples/rest-api/README.md) — общий
in-memory каталог с внешним JWT. У всех starter quotas/sessions process-local.
Несколько replicas требуют application shared state и отдельной приёмки.

Пример ниже — Basic на Linux. Нужны Node/npm, TLS proxy на той же машине и заранее
подготовленный PHC-файл по [Basic guide](../../../examples/basic/README.md).

```bash
node scripts/basic.mjs build
export APP_ENV=production
export BASIC_ADDRESS=127.0.0.1:8080
export BASIC_USERNAME=admin
export BASIC_PASSWORD_HASH_FILE=/run/secrets/basic.phc
export BASIC_ALLOWED_HOSTS=app.example.com
export BASIC_TRUSTED_PROXIES=127.0.0.1/32
.cache/basic/basic
```

Рабочий каталог — корень checkout, где build создал `public/.vite/manifest.json`.
В runtime перенесите binary и весь `public`, а не отдельный hash filename. На
Windows используется `.cache/basic/basic.exe`, переменные задаются через `$env:`.
SQL Admin собирается `node scripts/admin.mjs build`, затем отдельными командами
выполняются `migrate` и первоначальный `seed`; secrets и DSN приведены в его guide.
REST не требует Node: `go build -o .cache/rest-api ./examples/rest-api`.

## TLS proxy и остановка

На proxy задайте внешний origin `https://app.example.com`, backend
`http://127.0.0.1:8080`, заменяйте входные X-Forwarded-* значениями проверенного
client IP, `https` и внешней authority. Не добавляйте клиентский произвольный Host
в trusted forwarding chain. Если proxy в другом контейнере, используйте его
фактический узкий CIDR и закрытый network, вместо доверия всей локальной сети.
AllowedHosts содержит точный host с нестандартным портом, если такой есть.

Проверьте вход/выход через внешний HTTPS origin: Secure cookie не должна
отправляться по HTTP; direct backend с посторонним Host должен получить отказ.
Не публикуйте backend в интернет. Не выводите passwords, PHC, bearer/cookies в logs.

Для собственного сервиса [HTTP snippet](../getting-started.md#первый-http-сервис)
показывает отдельные `/livez` и `/readyz`. У Basic/Admin эти endpoints не заявлены;
добавляйте health probes через `web.NewHealth` при адаптации стартера. Readiness
зависит от lifecycle и bounded dependency checks; liveness не должна падать только
из-за временного отказа SQL. Management endpoints размещайте в закрытой сети.

При SIGTERM readiness снимается до drain. Grace period платформы должен превышать
общий Application shutdown budget, а broker lease — job/settlement budget с запасом.
В HTTP snippet это 7s общего cleanup и 5s HTTP shutdown: задайте grace больше 7s.
После drain завершите exporter через независимый cleanup context. При rolling
update не смешивайте несовместимые session formats/namespaces. Перед выпуском
сделайте backup, отдельно примените [миграции](migrations.md) и проверьте
[API migration guide](../api-migration.md).

Проверка browser production HTTPS уже входит в `sh scripts/basic-e2e.sh`; процедура
не подтверждает ваши proxy certificates/firewall, backup или SLO. Контракты:
[support](../support.md), [lifecycle](../lifecycle.md), [observability](../observability.md).
