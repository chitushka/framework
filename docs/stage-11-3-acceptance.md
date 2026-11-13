# Приёмка 11.3 — REST starter

Дата: 13.11.2026. Шаг закрывает REST starter, не объявляет весь framework
production-ready v1.0. [Контракт и запуск](../examples/rest-api/README.md),
[OpenAPI](../examples/rest-api/openapi.json).

## Реализация

- Внешний Ed25519 JWT verifier из `auth`, обязательные PEM/kid/issuer/audience,
  отсутствие issuer endpoint, fallback/client secrets; bounded key-file read.
- Полный CRUD статей: POST/GET/list/PUT/DELETE, строгий input только с title,
  запрет массового присваивания ID, body 4096 bytes и title 200 UTF-8 bytes.
- Единые `{error:{status,code,message}}` responses на auth, validation, media,
  router, proxy, capacity, panic и server resource boundaries. `WWW-Authenticate`,
  `Allow`, `Retry-After` сохраняются; internal details не раскрываются.
- Pagination limit/offset, filter title, sort allowlist, стабильный ID tie-break,
  snapshot/total и пустые страницы. Неизвестные/повторные query keys отвергаются.
- Process-local fixed-window IP/verified-subject quotas, bounded hashed-key
  tables и fail-closed capacity; доверие forwarded headers только явным CIDR.
- Embedded OpenAPI 3.1 и публичный `/openapi.json`, версия `/v1`, явная миграция
  прежнего demo и правила идемпотентности/retry/backward compatibility.
- `web.WithServerHandlerWrapper` позволяет нормализовать ошибки внешнего
  server resource layer; wrapper не читает body до внутренних limits.
  Добавлена отдельная network regression этого API.
- CI: отдельный REST lint/race/contract/process job с JSON artifacts и Windows
  process E2E. Remote CI/branch protection локальным запуском не подтверждаются.

## Доказательства

[Contract tests](../examples/rest-api/contract_test.go) проверяют CRUD/PUT retry/
DELETE retry, allowlists, pagination/filter ordering, auth failure/panic,
400/401/404/405/413/415/422/429/500/503, forged forwarded headers, Host rejection,
storage/limiter capacity, expiry и конкурентный CRUD/admission.
[JWT tests](../examples/rest-api/main_test.go) проверяют tampered/expired/revoked
keys и отсутствие встроенного issuer. [Config tests](../examples/rest-api/config_test.go)
проверяют обязательные значения, malformed limits/proxy/hosts и oversized PEM.

[OpenAPI response contracts](../examples/rest-api/openapi_test.go) сопоставляют
реальные success/error JSON с используемыми Schema keywords: refs, required,
types, bounds, array sizes, additionalProperties. Это ограниченный checker
конкретной спецификации, не универсальная OpenAPI certification.

[Process E2E](../examples/rest-api/e2e_test.go) собирает настоящий binary,
создаёт внешний Ed25519 key/token, запускает production config на TCP, проверяет
missing/tampered/expired JWT, CRUD, malformed input, missing/method/debug paths,
subject rate limit, JSON errors внешнего server layer (413 для 5 KiB и 9 MiB,
415 malformed Content-Type) и shutdown. Linux отправляет SIGTERM; Windows
завершает child process через Kill. [Server wrapper regression](../web/server_wrapper_test.go)
доказывает, что внешний wrapper видит server-generated 413 и oversized request
не достигает приложения.

Повторение из корня checkout:

```bash
go vet ./...
go test ./...
go test -race -tags e2e -count=1 ./examples/rest-api
golangci-lint run --build-tags e2e ./examples/rest-api/...
sh scripts/check-coverage.sh coverage.out 80
```

На Windows без C compiler уберите `-race`. Полный прежний lint debt и negative
coverage-script checks остаются в A4/12. Frontend и внешние SQL/cache/queue
adapters в этом шаге не менялись; их предыдущая приёмка не повторяется под видом
новой проверки. Результаты итогового локального прогона приведены ниже.

## Итог локального прогона

| Проверка | Результат |
| --- | --- |
| Windows `go vet ./...`, `go test ./...` | Прошли |
| Windows `go test -tags e2e -count=1 ./examples/rest-api` | Прошёл |
| Linux `go vet ./...` | Прошёл |
| Linux `go test -race -tags e2e ./...` | Все root module packages прошли; REST включая process E2E — 7,753s |
| Linux core coverage gate | 84,6% при required 80% |
| `golangci-lint v2.13.0 --build-tags e2e ./examples/rest-api/...` | 0 замечаний |
| Lint diff `web/server.go` и нового network test (`--new-from-patch`) | 0 замечаний |
| actionlint, gofmt, git diff check, относительные Markdown links изменённых guides | Прошли |

Go 1.26.11 на Windows и Linux. Linux проверен на snapshot исходников внутри
локального Docker image, с уже загруженными закреплёнными зависимостями и
`GOPROXY=off`. Первая попытка загрузки новых dependency volumes получила TLS
timeout; bind-mount прогон заменён snapshot из-за медленного Windows filesystem.
Итоговые успешные vet/race/coverage/lint выполнялись последовательно на одном
snapshot. Runtime SQL/cache/queue integrations этому in-memory starter не нужны.

## Границы

Каталог общий для всех токенов выделенного audience: issuer предоставляет полное
право CRUD каждому такому caller. Per-user isolation и read-only scopes нет.
Memory storage и quotas ограничены, но теряются при restart; несколько replicas,
durable deduplication, backup/recovery и continuous key rotation не поставляются.
CLI читает один key при запуске; SDK `ReplaceKeys` доступен application code.
POST неидемпотентен; PUT last-writer-wins, DELETE повторно возвращает 404.
Header/body/timeouts самого HTTP transport могут завершаться без JSON; application
resource/proxy/router/auth errors нормализованы. Early resource/proxy/debug
rejection может предшествовать quota. Deployment отвечает за TLS и edge limits.

Следующий шаг плана — 11.4, руководство и onboarding.
