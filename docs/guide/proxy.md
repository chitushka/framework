# Proxy trust, Host, redirects и CORS

`Router` по умолчанию игнорирует Forwarded/X-Forwarded-* и принимает любой синтаксически
корректный Host. `Context.ClientIP()` использует непосредственного peer, `Scheme()` — TLS
соединения, `Host()` — проверенный authority. Пустой/повреждённый Host отклоняется с 400
до route matching и middleware. Для production задайте явный host allowlist.

## Настройка границы

Для Router без внешних middleware используйте `router.SetProxyPolicy(web.ProxyConfig{...})`.
Когда CORS, logging или другие handlers находятся снаружи Router, оберните их через
`web.NewProxyHandler(handler, config)`. Этот handler должен находиться **снаружи CORS**,
чтобы preflight не обходил Host policy и same-origin использовал внешний scheme/host.

```go
cors, err := web.NewCORSHandler(router, web.CORSConfig{
    AllowedOrigins:   []string{"https://ui.example.com"},
    AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
    AllowedHeaders:   []string{"Content-Type", "Authorization", "X-CSRF-Token"},
    ExposedHeaders:   []string{"X-Total-Count"},
    AllowCredentials: true,
    MaxAgeSeconds:    600,
})
if err != nil {
    return nil, err
}
return web.NewProxyHandler(cors, web.ProxyConfig{
    TrustedProxies:         []string{"10.20.0.10/32", "2001:db8:1234::10/128"},
    ForwardedHeader:        "Forwarded",
    AllowedHosts:           []string{"app.example.com"},
    AllowedRedirectOrigins: []string{"https://login.example.com"},
})
```

Результат передаётся в `web.NewServer`. Resource limits сервера из шага 7.1 остаются
внешней защитой. Router наследует уже разрешённый origin. При вложенных явно заданных
proxy policies первый handler определяет IP/scheme/host, последующие проверяют свои
host allowlists и сужают разрешённые redirect origins. Они не могут заново довериться
заголовкам, отвергнутым внешней границей.

`TrustedProxies` содержит CIDR именно proxy, которые оператор контролирует. Не включайте
в него всю клиентскую сеть или `0.0.0.0/0` ради удобства. Непосредственный peer берётся
из `Request.RemoteAddr`, а не из заголовка. IPv4-mapped peer нормализуется в IPv4;
IPv4-mapped CIDR в конфигурации запрещён — укажите обычный IPv4 CIDR.

`ForwardedHeader` выбирается явно: пустая строка игнорирует обе семьи, `Forwarded`
разбирает только RFC 7239, `X-Forwarded` — только X-Forwarded-For/Proto/Host. Автоматического
fallback или объединения семей нет. Входной reverse proxy должен удалять присланные клиентом
Forwarded/X-Forwarded-* и создавать достоверные значения. В цепочке контролируемых proxy
каждый добавляет только фактически наблюдённый предыдущий peer; последний proxy обязан
перезаписывать X-Forwarded-Proto/Host проверенными одиночными значениями, не пересылать
клиентский текст и не создавать списки этих двух полей.

X-Forwarded-For и Forwarded обходятся справа налево, пока текущий peer доверен.
Первый недоверенный IP становится client IP; левый префикс не используется для его подмены.
Forwarded host/proto берутся из **того же элемента**, что выбранный client IP; при их
отсутствии используются исходные transport scheme и wire Host. Для X-Forwarded Proto/Host
источником является непосредственно доверенный proxy, отдельно от IP chain. Отсутствие XFF
не запрещает эти одиночные metadata headers; client IP тогда остаётся peer IP.

Поддерживаемый строгий subset Forwarded требует `for` с IP в каждом используемом элементе,
поддерживает quoted IPv6/port и RFC quoted-pair. `unknown`/obfuscated nodes, дубли параметров,
повреждённые quotes, control characters и неподдерживаемый scheme отвергаются с 400.
Цепочка ограничена 32 элементами. X-Forwarded Proto/Host должны иметь ровно одно непустое
значение; comma lists/repeated fields отвергаются. От недоверенного peer даже повреждённые
proxy headers игнорируются. IPv6 zones не принимаются. Недоверенный префикс цепочки не
объявляется достоверным; синтаксическая ошибка всего Forwarded list может отклонить запрос.

## Host и redirects

Host policy проверяет authority после proxy resolution и до маршрутизации; host routes
видят именно этот `Request.Host`. DNS переводится в lowercase, один trailing dot удаляется,
IPv6 требует brackets, порт — число 1–65535. Порт является частью host allowlist:
`app.example.com` и `app.example.com:8443` — разные entries. Wildcards, userinfo, paths,
пустые ports, scoped IPv6 и Unicode hostname запрещены; IDN задавайте в punycode.

`Request.RemoteAddr`/`TLS` остаются сведениями о соединении, URL.Scheme/URL.Host не объявляются
проверенными. Для security решений используйте `Context.ClientIP()`, `Scheme()`, `Host()`;
самостоятельное чтение raw forwarding headers обходит этот контракт.

`Context.Redirect` по умолчанию принимает только root-relative пути вроде `/account?tab=1`.
Bare relative, network-path `//host`, backslashes (включая encoded), control characters,
userinfo и non-HTTP schemes запрещены. Внешний HTTP(S) redirect допускается только в точный
`AllowedRedirectOrigins`; scheme/port учитываются, subdomains не наследуют разрешение.
Same-origin absolute URL тоже требует явного разрешения. Origin нормализует case/default
port, но trailing-dot origins не принимаются: это отдельные browser origins. При отказе —
400 без Location и без начала redirect response. Это меняет прежнее поведение API, которое
принимало произвольный location; используйте root-relative путь или добавьте нужный origin.

## CORS

CORS является HTTP wrapper, а не route middleware: успешный preflight OPTIONS возвращает
204 до router/auth handlers. Существование endpoint и его авторизация проверяются actual
request; разрешённый preflight не означает наличие маршрута. Actual/error/404 responses
для разрешённого origin сохраняют CORS headers. Запрос без Origin проходит в приложение;
same-origin запрос тоже проходит без CORS grants, с учётом proxy-resolved scheme/host.

Cross-origin requests требуют точного HTTP(S) origin из allowlist. Пустой список запрещает
cross-origin requests, `null`/opaque origins не поддерживаются. Полученный Origin должен
быть canonical serialized origin без path, slash, fragment или default port; entries
конфигурации канонизируются. `*` разрешён только без credentials; с credentials отражается
проверенный origin и ставится `Access-Control-Allow-Credentials: true`.

AllowedMethods по умолчанию GET/HEAD/POST, имена методов case-sensitive. AllowedHeaders и
ExposedHeaders — явные HTTP header tokens, сравнение имён case-insensitive; wildcard для них
не поддерживается. Preflight проверяет requested method и каждый requested header, запрещает
повторный Access-Control-Request-Method и повреждённые списки. Отказ — 403 без CORS grants.
OPTIONS без preflight method не перехватывается как preflight; при Origin к нему применяется
обычная method policy. MaxAgeSeconds: 0–86400, ноль не добавляет Max-Age.

`Vary: Origin` добавляется даже при отсутствии Origin и при отказах; OPTIONS дополнительно
варьируется по Access-Control-Request-Method/Headers. Существующие Vary tokens и `*` сохраняются,
повторное добавление того же имени не производится. Не добавляйте противоречащие CORS headers
в других handlers. CORS не является authentication, authorization или заменой CSRF.

## Проверенные сценарии

| Сценарий | Ожидаемый результат |
| --- | --- |
| Недоверенный peer подделывает обе семьи, включая malformed headers | IP/scheme/host остаются transport metadata |
| Прямой TLS запрос с X-Forwarded-Proto=http | Scheme остаётся https |
| Доверенный proxy, выбранная семья X-Forwarded | Проверенные singleton proto/host, IP из правой доверенной части XFF |
| Spoofed left prefix и недоверенный промежуточный IP | Обход останавливается на этом IP |
| Forwarded chain с разными host/proto на каждом hop | Metadata принадлежат выбранному IP element |
| Quoted IPv6 с port; IPv4-mapped peer | IP корректно нормализован, CIDR проверен |
| Две семьи, но выбрана одна | Вторая не влияет на результат |
| Повреждённый trusted header, повторные поля, длинная цепочка | 400 до handler |
| Запрещённый/повреждённый Host, в том числе preflight | 400 до routing/CORS grant |
| Network-path, encoded slash/backslash, внешний redirect вне списка | 400 без Location |
| Разрешённый credentialed preflight | 204, exact Origin, credentials, method/headers/max-age и Vary |
| Неизвестный/opaque Origin, запрещённый method/header | 403, без CORS grants |
| No-Origin, same-origin, error/404, wildcard и существующий Vary | Не теряются обычные HTTP/error semantics и cache variation |

Regression tests: [proxy](../../web/proxy_test.go), включая реальные TCP connections,
и [CORS](../../web/cors_test.go). Browser E2E проверяется отдельно; текущая HTTP writer conformance
описана в [HTTP compatibility](http-compatibility.md). Нормативные источники: [RFC 7239](https://www.rfc-editor.org/rfc/rfc7239.html)
и [Fetch CORS protocol](https://fetch.spec.whatwg.org/#http-cors-protocol).
