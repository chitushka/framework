# HTTP compatibility contract

Шаг 7.3 задаёт семантику маршрутизации, файловых ответов и ResponseWriter wrappers.
Она проверяется обычными tests и реальными HTTP/1.1/TLS HTTP/2 connections, включая
observability → session/debug → handler. Произвольные сторонние wrappers требуют
собственной проверки; они должны сохранять Unwrap и нужные transport capabilities.

## Методы и пути

| Запрос | Поведение Router |
| --- | --- |
| HEAD с отдельным подходящим HEAD route | Вызывает его, даже если GET зарегистрирован раньше |
| HEAD без отдельного HEAD, но с подходящим GET | Вызывает GET action с неизменённым Request.Method=HEAD |
| OPTIONS известного path/host без отдельного OPTIONS | 204 с отсортированным Allow, без вызова action/middleware |
| OPTIONS с отдельным route | Обычная цепочка middleware/action; ответ задаёт приложение |
| Неподдерживаемый method известного path/host | 405 с Allow; GET добавляет HEAD, известный path добавляет OPTIONS |
| Неизвестный path/host, включая HEAD/OPTIONS | 404; HEAD не содержит body |

Allow учитывает host и ограничения параметров. Automatic OPTIONS не является CORS grant
или проверкой авторизации; CORS из шага 7.2 работает снаружи Router и отдельно проверяет
Origin/method/headers. Для глобального `OPTIONS *` у `web.Server` действует стандартный
general OPTIONS handler net/http; per-path Allow описан таблицей выше.

HEAD responses через Context helpers не содержат body. HTML/JSON/XML сохраняют размер
сериализованного представления в Content-Length; `Stream` не читает source reader для HEAD.
Файловые HEAD headers формирует ServeContent. Стандартный HTTP server также подавляет body
для прямых ResponseWriter writes; приложениям следует избегать чтения дорогого stream для HEAD.
204/304 и informational responses не получают payload от Context helpers.

Request path делится на сегменты **до** percent decoding, затем каждый сегмент декодируется
ровно один раз. `/items/a%2Fb` соответствует `/items/{id}` с id=`a/b`; это не два сегмента
и не совпадение с `/items/a/b`. `/items/a%252Fb` даёт id=`a%2Fb`. Where проверяет decoded
value. Параметр остаётся недоверенным вводом приложения, в том числе при работе с filesystem.

Один trailing slash является alias: `/items/a` и `/items/a/` вызывают один route без redirect.
Повторные слеши, сегменты `.`/`..` (включая encoded), backslash, control bytes и invalid UTF-8
отвергаются с 400; router не чистит их и не перенаправляет запрос. Повреждённый URL может
быть отвергнут net/http ещё до Router. Query string не участвует в path matching.

Patterns задаются decoded литералами и `{parameter}`; query/fragment markers в pattern
не принимаются. URL generation экранирует и literals, и parameter values; slash в значении
становится `%2F`. Dot/control/backslash values, которые Router отверг бы, не генерируются.
Trailing-slash aliases одного method/host нельзя зарегистрировать дважды. Эти правила
ужесточают прежнюю обработку повторных слешей и исправляют round trip encoded parameters.

## File и Download

При status=200 и наличии Request методы используют `http.ServeContent` поверх открытого
regular file. Поддерживаются HEAD, Last-Modified, byte/suffix/multiple ranges,
If-Modified-Since, If-Unmodified-Since, If-Match, If-None-Match и If-Range по семантике Go.
Типовые ответы: 200, 206, 304, 412, 416. Content-Length/Content-Range/Accept-Ranges
формируются для выбранного представления, а не заранее для полного файла.
Range применяется только к GET: HEAD с Range сохраняет headers полного представления.

Фреймворк не вычисляет ETag автоматически. Если он нужен, приложение устанавливает
корректный ETag до `ctx.File`/`Download`. Иначе доступны Last-Modified/date validators.
Download сохраняет безопасно сформированный Content-Disposition и filename.
Статус, отличный от 200, или вручную созданный Context без Request использует обычный
file stream с заданным статусом; Range/preconditions в этом режиме не применяются.
Ошибки записи сохраняются для errors.Is/errors.As. После начала ответа нельзя заменить
уже отправленные bytes новым JSON error.

## Streaming и wrappers

`Context.Stream` отправляет headers сразу и делает flush после каждой записи из reader.
На writer без flush capability копирование остаётся доступным; другие flush errors
возвращаются вызывающему коду. Reader должен завершаться или учитывать cancellation:
фреймворк проверяет context между writes, но не может прервать произвольный блокирующий Read.
Владение reader/его Close остаётся у вызывающего кода. Для ручного flush cadence используйте
Writer и ResponseController. Deadlines задаются согласно [resource policy](web.md#timeouts-и-streaming).

Session, CookieStore, observability и debug сохраняют Unwrap. `http.Flusher` и `http.Hijacker`
доступны через type assertion только при наличии у underlying chain; HTTP/2 не объявляет
Hijacker. ResponseController.Flush проходит через interceptors, возвращает ошибку flush/commit
и не обходит session commit или debug buffer. SetReadDeadline/SetWriteDeadline и HTTP/1
EnableFullDuplex доходят до transport через Unwrap. HTTP/2 уже duplex; отсутствие отдельного
EnableFullDuplex метода у transport может возвращать http.ErrNotSupported.

Session изменения сохраняются перед первым final header/body, flush или hijack.
Informational 1xx (кроме 101) не фиксируют session или окончательный status наблюдаемости.
Изменения session должны предшествовать началу вывода; после commit сессия заморожена.
Конкурентные snapshots/revocation описаны в [session contract](sessions.md).
Конфликт commit возвращает 409, backend failure до реального вывода возвращается как 500,
а middleware восстанавливает исходный writer для error handler.
Неотправленные representation headers (например Content-Length/Range/Encoding/ETag от файла)
очищаются перед error handler, чтобы они не исказили framing и validators нового error body.

После hijack caller владеет connection, deadlines, HTTP upgrade handshake и закрытием.
Если handshake должен включать session cookie, caller сериализует необходимые headers из
Writer.Header() в свой ответ. net/http shutdown не управляет hijacked connection. Байты,
написанные напрямую в socket после hijack, не проходят через HTTP logging/byte counters;
ручной handshake не задаёт наблюдаемости автоматически его status.

Debug toolbar буферизует только до `Config.MaxResponseBytes` (default 1 MiB). Превышение
порога, flush или hijack переводит writer в постоянный passthrough без инъекции toolbar.
HEAD, Range/partial responses, files с Accept-Ranges/Content-Disposition и представления
с ETag не модифицируются. При panic writer восстанавливается; необъявленный наружу buffer
не мешает Recover/error handler сформировать 500. `Context.Written()` в Router отражает
final output/hijack на его writer boundary, включая прямые writes. Для standalone Context
отслеживаются только вызовы response helpers. Защита от повторного вызова helpers сохраняется
и при ещё буферизованном ответе.

`web.WithServerHandlerWrapper` оборачивает полный handler управляемого HTTP server,
включая ответы внешнего resource limiter. Это позволяет задать application error
contract или instrumentation для ранних 413/415. Wrapper не должен читать body
перед вызовом next: body/form limits находятся внутри next. Wrappers применяются
в порядке объявления (последний — внешний). Нормализация REST responses не
поддерживает streaming/upgrades; для них требуется wrapper с сохранением нужных
writer capabilities. [Network regression](../../web/server_wrapper_test.go).

## Доказательства

- [HTTP methods, escaped paths, HEAD, ranges и validators](../../web/http_conformance_test.go).
- [Streaming/cookies/logging до завершения handler, disconnect, hijack, HTTP/2, 103,
  bounded debug buffer, panic и session commit failures](../../web/writer_chain_test.go).
- [Optional interface/error propagation и controller traversal](../../internal/httpwriter/writer_test.go).

Основные источники: [net/http ServeContent и ResponseController](https://pkg.go.dev/net/http@go1.26.0)
и [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html). Browser E2E, WebSocket protocol
implementation и проверка произвольных third-party wrappers не входят в этот contract.
