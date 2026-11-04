# Security audit HTTP/tooling, шаг 7.6

Аудит 04.11.2026 охватывает framework responses/templates/assets, SQL builder,
mass assignment, files, migration source, CRUD generator и observability.
Результат — code review и regression suite, не независимый pentest или
сертификация приложения. Модель deployment — в [support contract](support.md).

## Production-профиль

`web.NewProductionHandler` должен оборачивать всю публичную handler chain,
включая proxy/observability/router. Он ставит request-context marker, который
клиентские environment/headers/query не могут переключить. Framework toolbar,
даже с Enabled=true/CaptureSQLArgs=true, не собирает данные и не вызывает
Environment/Session callbacks. `/debug` и descendants возвращают 404, включая
encoded spelling; `observability.PprofHandler` блокируется и при другом mount.
Отдельные listeners/прямые pprof handlers защищает приложение: импорт pprof
регистрирует стандартные endpoints на DefaultServeMux.

Router в профиле использует generic JSON errors вместо custom SetErrorHandler.
Все 4xx/5xx messages — стандартные status texts. Во всех режимах HTTPError 5xx
больше не раскрывает Message, invalid error status превращается в 500. Error
responses имеют Cache-Control: no-store. Unhandled panic до output очищает
неотправленные headers/cookies и формирует generic 500; после output/hijack
вызывает http.ErrAbortHandler, без исходного panic в transport log. Уже
отправленные bytes не заменяются новым error body.

Профиль устанавливает nosniff, Referrer-Policy: no-referrer, X-Frame-Options: DENY
и сохраняет writer capabilities через Unwrap/ResponseController. TLS/HSTS,
Host/proxy allowlists, CSP, CORS, session policy, authentication и permissions
задаёт приложение. Произвольные application response bodies/headers профиль
не переписывает. Приложение должно сохранять исходный request context.

```go
// router уже содержит resource limits, proxy/Host policy, auth/CSRF и routes.
logHandler, err := logging.NewProductionHandler(os.Stdout)
if err != nil { return err }
observed := observability.HTTP(observability.HTTPConfig{
    Logger: slog.New(logHandler),
})(router)
public, err := web.NewProductionHandler(observed)
if err != nil { return err }
server, err := web.NewServer(web.ServerConfig{Address: ":8080"}, public)
```

Production HTTP logs/trace names/path attributes используют `[redacted]` вместо
raw URL path, panic spans — `request panic`. Стандартный HTTP logger не пишет
query strings, Authorization/cookies. Неизвестные HTTP methods сводятся к OTHER
в production telemetry и всегда в metrics. Method/status labels metrics теперь
имеют конечную cardinality; клиент не может бесконечно увеличивать registry.

`logging.NewProductionHandler` — JSON handler уровня Info. Sensitive attribute
keys/варианты (password/secret/token/credentials, Authorization/Cookie, DSN,
connection strings, private keys, error/exception/SQL/args) скрываются, включая
pre-bound attrs и sensitive groups. Opaque Any values (errors/structs/maps)
заменяются целиком на `[REDACTED]`. Fixed messages и несекретные примитивные attrs
сохраняются. Это не классификатор содержимого: секрет в message или безобидно
названной строке не распознаётся. Не передавайте passwords/tokens/error.Error()
конкатенацией в message. Другие handlers/exporters требуют собственной policy;
не переключайте global logger на каждом запросе.

## Files и tooling

`Context.File`/`Download` принимают доверенный application path. Join и
предварительный EvalSymlinks не гарантируют containment клиентского имени.
`FileFromRoot(status, root, name)` и
`DownloadFromRoot(status, root, name, downloadName)` используют `*os.Root`:
slash-separated relative names без dot traversal, absolute/Windows paths,
backslashes/control chars/ADS. OS удерживает root, включая symlink/rename races
на baseline Linux/Windows. Ссылки внутри root допустимы, наружу — нет. Caller
закрывает root после drain handlers. Range/HEAD/conditional/download сохраняются.

```go
root, err := os.OpenRoot(uploadDirectory) // trusted server-owned directory
if err != nil { return err }
// Владение root сохраняется на срок жизни handlers; закрыть при shutdown.
return ctx.DownloadFromRoot(http.StatusOK, root, ctx.Param("name"), "download.bin")
```

Containment не заменяет authorization/tenant mapping. Недоверенные uploads
отдавайте attachment или с отдельного origin: inline HTML/SVG/JS может исполнять
активное содержимое. Root не защищает от привилегированного локального attacker,
hard links, bind mounts и special devices; serving directory принадлежит серверу.

DirectorySource теперь читает SQL через открытый os.Root и отвергает external
symlinks. SQL files остаются доверенным release code: root не sanitise SQL.
CLI output directories/templates принадлежат оператору, не HTTP-клиенту.
WriteCRUD использует O_EXCL для leaf files, но не предназначен для работы в
каталогах, изменяемых локальным attacker. Полная generator compilation/dry-run/
recovery приёмка остаётся для 8.4; generation не публикует приложение автоматически.

CRUD Route проверяется как root-relative local path без query/fragment, control
chars, backslashes, повторных separators и dot traversal. Vue endpoint теперь
кодируется JSON с HTML escaping: `</script>` не закрывает SFC script. Имена
package/model/columns валидируются отдельно.

## Threat-model checklist

| Граница | Результат / доказательство |
| --- | --- |
| HTML/layout/widget и Vue JSON breakout | Закрыто для обычных данных через html/template/JSON escaping: [view tests](../../web/view_test.go). template.HTML/JS, raw HTML и funcs — trusted escape hatches |
| Asset event attributes / sources | Event attributes и srcdoc явно отвергаются; URL contexts экранирует html/template: [regressions](../../asset/security_test.go), [asset tests](../../asset/asset_test.go). Script origins выбирает приложение |
| Generated Vue с malicious Route | SFC script breakout закрыт, external/query/traversal paths отвергаются: [generator regressions](../../gii/security_test.go) |
| SQL identifiers / values / raw | Quote escaping проверен для 4 dialects, values остаются args: [audit tests](../../db/security_test.go), [builder tests](../../db/query_test.go). RawCondition/SelectRaw — trusted fragments, не sanitizer/SQL parser; filter/order/column names требуют application allowlist |
| Mass assignment / privilege fields | ID/Role/unknown fields не назначаются вне scenario allowlist, conversion error не меняет модель: [assignment](../../validation/assignment_test.go), [conversions](../../validation/conversion_test.go). BindJSON domain struct и generated writable DTO сами не задают authorization/role policy |
| Traversal / symlink / file Range | Закрыто для Root API; trusted File API явно отделён: [network/root tests](../../web/security_audit_test.go). Linux выполняет symlink tests обязательно; Windows без privilege пропускает только соответствующий subtest |
| Migration source symlink | External SQL не загружается: [source regression](../../db/migration/source_security_test.go) |
| Open redirect / proxy / Host / CORS | Действует контракт 7.2: root-relative или explicit origins, запрет scheme-relative/decoded controls/backslash: [proxy tests](../../web/proxy_test.go), [CORS](../../web/cors_test.go), [policy](proxy.md) |
| Error/status/panic disclosure, custom handlers, debug/pprof | Закрыто внутри production chain, diagnostic callbacks отключены: [production regressions](../../web/security_audit_test.go) |
| Secrets в logs/telemetry | Sensitive keys/groups/opaque values скрыты, raw paths/panics не экспортируются: [logging](../../logging/production_test.go), [profile/telemetry](../../web/security_audit_test.go). Fixed messages и безопасные application attrs обязательны |
| Method label memory exhaustion | Неизвестные methods объединены в OTHER: [10000-method regression](../../observability/security_test.go) |

Вне этой приёмки: полная SQL conformance, migration lock/dirty recovery, внешние
session adapters, queue durability и process lifecycle — следующие шаги плана.

Основания: [Go traversal-resistant APIs](https://go.dev/blog/osroot),
[OWASP error handling](https://cheatsheetseries.owasp.org/cheatsheets/Error_Handling_Cheat_Sheet.html).
