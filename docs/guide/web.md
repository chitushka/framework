# Web-приложение

## Маршруты

Регистрируйте маршруты при сборке приложения. Имена нужны для обратной генерации URL, а `Where` ограничивает параметры при разборе и генерации.

```go
err := router.GET("/users/{id}", showUser,
    web.WithName("users.show"),
    web.Where("id", `[0-9]+`),
)
```

Группы объединяют prefix, host и middleware. Разделяйте `/api`, `/admin` и публичную часть группами, а не проверками внутри каждого handler.

Доверие к proxy, Host allowlist, безопасные redirects и CORS настраиваются до маршрутизации;
см. [контракт proxy trust и таблицу сценариев](proxy.md). Без явного доверия forwarding headers
не меняют `Context.ClientIP()`, `Scheme()` и `Host()`.

## Middleware

Middleware применяется в порядке регистрации и должно либо вызвать `next`, либо вернуть ответ/ошибку. Рекомендуемый внешний порядок: recovery, request ID/logging, security headers, session, authentication, authorization, handler.

Middleware не должно сохранять request-specific данные в полях общего объекта без синхронизации. Используйте контекст запроса.

## Controllers и actions

Для небольшого endpoint достаточно `web.Handler`. Controller удобен, когда actions используют общие hooks. Зависимости action разрешаются через DI на каждый запрос; `BeforeAction` может отменить вызов, `AfterAction` видит его ошибку.

## Ввод и ответы

`BindJSON` принимает ровно одно JSON-значение (в том числе массив или scalar, если destination
это допускает) и отклоняет неизвестные поля struct. Требуется `Content-Type: application/json`
или `application/*+json` (допускаются параметры вроде `charset=utf-8`), иначе возвращается 415.
Router и Server ограничивают body до декодирования. После декодирования выполняйте validation
до изменения постоянного состояния.

## Ограничения ресурсов HTTP

`Router` применяет `RequestLimits` до route/global middleware и разбора форм. `Server`
применяет ту же политику снаружи переданного `http.Handler`, поэтому она действует и для
обычных handlers, и для middleware вне Router. Для самостоятельного net/http handler без
`web.Server` используйте `web.NewLimitedHandler(handler, limits)` снаружи читающих body middleware.
`Context`, созданный вручную, сам по себе body limiter не устанавливает.

| RequestLimits | По умолчанию | Семантика |
| --- | --- | --- |
| MaxBodyBytes | 8 MiB | Всё тело, включая multipart framing; известный Content-Length проверяется до handler, chunked/неизвестная длина ограничиваются при чтении |
| MaxFormBytes | 1 MiB | Encoded URL-form body; для multipart — сумма размеров имён/значений полей после разбора |
| MaxMultipartMemory | 1 MiB | Порог file data в памяти, остальное — temporary files; это не общий memory limit parser |
| MaxUploadBytes | 4 MiB | Максимальный размер каждого файла, проверяется после bounded multipart parsing |
| MaxFormValues | 1000 | Количество значений form, включая query values на form-запросах |
| MaxFiles | 16 | Количество загруженных файлов |

Ноль в поле выбирает конечный default, отрицательное значение отклоняется. Общее тело
ограничивается `http.MaxBytesReader` **до** JSON/form parsing. URL-encoded формы дополнительно
получают меньший byte limit. Form/file metadata проверяются после bounded parsing,
но до middleware: превышение возвращает 413. Повреждённый body возвращает 400;
ошибка MIME syntax — 415; body read timeout при разборе — 408. Для необработанных streams
handler должен проверять ошибки чтения: игнорирование ошибки не превращается в автоматический 413.
Непрочитанные неизвестной длины тела не сканируются заранее. Автоматической декомпрессии нет;
распаковку и лимит распакованных данных задаёт приложение.

URL-encoded и multipart формы разбираются заранее; используйте `Request.Form`,
`Request.PostForm` и `Request.MultipartForm`. Не перечитывайте их body как raw stream.
Для multipart библиотека сохраняет и свои внутренние ограничения parts/headers; повышение
`MaxFiles` не отменяет этих ограничений. После возврата handler temporary uploads удаляются,
включая ошибки, panic и disconnect. Открытые upload files закрывайте в handler, нужные данные
копируйте в постоянное хранилище до его завершения. Не передавайте `FileHeader` фоновой задаче.

Пример общей настройки (при вложенных ограничителях действует более строгая политика):

```go
limits := web.RequestLimits{MaxBodyBytes: 16 << 20, MaxUploadBytes: 8 << 20}
router := web.NewRouter()
if err := router.SetRequestLimits(limits); err != nil {
    return err
}
server, err := web.NewServer(web.ServerConfig{RequestLimits: limits}, router)
```

Если повышаете лимит, настройте и Server, и Router. Более низкий внешний лимит нельзя
расширить во внутреннем handler. До wrapper нельзя передавать уже разобранное недоверенное
тело: предшествующие middleware уже могли выделить память или записать файлы.

### Timeouts и streaming

| ServerConfig | Default |
| --- | --- |
| ReadHeaderTimeout | 5 секунд |
| ReadTimeout | 15 секунд на чтение запроса, включая body |
| WriteTimeout | 30 секунд для обычных ответов |
| IdleTimeout | 60 секунд между keep-alive запросами |
| ShutdownTimeout | 10 секунд |
| MaxHeaderBytes | 16 KiB по семантике net/http, включая его parsing reserve |

Ноль выбирает default, отрицательные значения запрещены. Header limit обрабатывает сам
net/http (431, формат ответа может отличаться от JSON); слишком медленные неполные headers
могут приводить к закрытию соединения без JSON-ответа. Настройки proxy должны согласовываться
с этими бюджетами. Таймаут записи не прерывает вычисления handler: учитывайте request context
и задавайте сроки зависимых операций.

Для SSE/долгих ответов используйте отдельный listener/server с `ServerConfig{Streaming: true}`.
В этом режиме нулевой `WriteTimeout` означает отсутствие общего write deadline;
явно заданный положительный `WriteTimeout` по-прежнему действует. Read/header/idle/body limits
остаются конечными. Handler должен ограничивать каждую запись и проверять disconnect:

```go
controller := http.NewResponseController(ctx.Writer)
if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
    return err
}
// Записать очередной chunk, затем проверить ошибку Flush.
if err := controller.Flush(); err != nil {
    return err
}
```

Перед каждым следующим chunk обновляйте write deadline и проверяйте `ctx.Request.Context()`.
Framework wrappers сохраняют ResponseController/Unwrap и доступные Flusher/Hijacker.
`Context.Stream` копирует reader с flush headers и каждой записи; deadlines задаёт handler.
Проверенная цепочка session/debug/logging и ограничения описаны в [HTTP compatibility](http-compatibility.md).
Контракт основан на [net/http](https://pkg.go.dev/net/http@go1.26.0) и
[multipart](https://pkg.go.dev/mime/multipart@go1.26.0); regression tests —
[limits](../../web/limits_test.go) и [server policy](../../web/server_policy_test.go).

**Изменение совместимости:** клиенты, ранее отправлявшие JSON без Content-Type, теперь получают
415. Долгие обычные запросы и большие загрузки должны явно настроить бюджеты. Учебные тесты
обновлены для отправки JSON с правильным media type; отменять лимиты нулевыми значениями нельзя.

Используйте специализированные методы `HTML`, `JSON`, `XML`, `File`, `Download`, `Stream` и `NoContent`.
Они отслеживают начало ответа и задают content type. Router также учитывает прямые writes,
flush и hijack; ошибка после начала stream не может отменить отправленные байты.
Публичные сообщения об ошибках задавайте через `web.NewHTTPError`; внутреннюю причину передавайте
третьим аргументом для логирования и `errors.Is`. HEAD/OPTIONS/405/Allow, encoded paths,
file Range/conditional responses и сетевые проверки описаны в [HTTP compatibility contract](http-compatibility.md).

## Views и frontend

`web.Renderer` читает templates из `fs.FS`, поддерживает layouts и widgets. Данные для Vue сериализуются через `vueState`, что защищает HTML-контекст. Assets регистрируются через `asset.Registry`; production bundle строится из Vite manifest, development bundle подключает HMR server.

Не вставляйте пользовательские строки в `template.HTML`. Статические файлы отдавайте отдельным handler или reverse proxy.
