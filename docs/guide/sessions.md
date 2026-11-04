# Sessions и CSRF: контракт и миграция

## Атомарное серверное хранилище

`session.New` требует `AtomicBackend`. `CompareAndSwap` сравнивает точные bytes
загруженного снимка и одной атомарной операцией заменяет запись или переносит её
на новый ID. При переносе нельзя перезаписывать занятый новый ID. `nil` expected
означает отсутствующую запись; пустой newID — удаление. Истёкшая запись считается
отсутствующей. CAS, Save, Delete и TTL expiration должны иметь общий порядок
для всех экземпляров приложения. Get/Set или mutex одного процесса недостаточны
для внешнего хранилища.

Встроенный `MemoryBackend` атомарен: два Managers могут разделять один экземпляр,
но разные процессы его память не разделяют. Внешний общий AtomicBackend
предоставляет приложение. Текущие cache Redis/SQL/Memcached adapters не реализуют
атомарный перенос и отклоняются `session.NewBackend`; готовые сетевые session
adapters не входят в подтверждённый baseline (этап 9.1).

Каждый запрос с существующей сессией обновляет idle deadline, включая чтение.
Два commits одного снимка конфликтуют: поздний получает HTTP 409 и
`errors.Is(err, session.ErrConflict)`, без новой cookie. Merge/retry отсутствуют.
Сериализуйте запросы одного браузера, меняющие session. CAS не откатывает SQL,
письма и jobs, выполненные handler до commit; повтор бизнес-операции после 409
требует собственной идемпотентности. Это optimistic commit policy, а не блокировка handler.

`Regenerate` сохраняет значения, атомарно переносит запись и очищает CSRF token.
`auth.Login`/`Logout` вызывают его. Старый ID после успешного commit больше не
загружает прежнее состояние; запрос, уже загрузивший его, не сохранит снимок
обратно. Неизвестный/просроченный ID заменяется новым случайным ID. `Destroy`
удаляет текущий ID и истекает cookie; backend error возвращается вызывающему коду.
Отзыв одного ID не отзывает все устройства/учётную запись и не прерывает уже
начавшиеся действия handler — для этого нужны application checks.

Commit происходит перед первым final header/body, flush или hijack. После него
Set/Regenerate возвращают `ErrCommitted`, Delete/Destroy не меняют состояние.
Меняйте сессию до начала ответа. Commit failure до вывода идёт в error handler,
без раскрытия backend errors клиенту.

## Idle/absolute TTL и cookie policy

`Config.TTL` — idle timeout (default 24 часа), `AbsoluteTTL` — общий предел
(default 7 дней). Оба значения не меньше секунды. Сервер проверяет сроки при
загрузке и commit; backend TTL и cookie MaxAge ограничены ближайшим deadline.
Read-only запрос обновляет idle timeout, Regenerate не сбрасывает absolute timeout.
Handler, пересёкший deadline, получает 409. Часы экземпляров синхронизируются.

```go
sessions, err := session.New(backend, session.Config{
    Name:        "__Host-session",
    Secure:      true,
    Path:        "/",
    TTL:         30 * time.Minute,
    AbsoluteTTL: 12 * time.Hour,
    SameSite:    http.SameSiteLaxMode,
})
```

HttpOnly включён всегда, default SameSite — Lax. SameSite=None требует Secure;
`__Secure-` требует Secure, `__Host-` также требует Path=/ и отсутствие Domain.
Secure задаётся deployment config, не request header. Local HTTP examples
оставляют Secure=false для разработки. TLS/SameSite не заменяют CSRF. Все
экземпляры используют одинаковые cookie/lifetime policies.

## Stateless CookieStore и ротация

CookieStore предназначен для небольшого некритичного состояния. AES-GCM
аутентифицирует version, key ID, cookie name и envelope с timestamps/values.
Idle/absolute timeout проверяется сервером; чтение продлевает idle lifetime.
Value ограничен 3800 bytes, полная сериализованная cookie — 4096 bytes.
Oversized input не расшифровывается, oversized output вызывает ошибку.

`NewCookieStore(secret, config)` использует ключ `primary`.
`NewCookieStoreWithKeys` принимает 1–4 `CookieKey`: первый выпускает cookies,
остальные только читают. ID уникален, содержит 1–32 ASCII буквы/цифры/`-_`;
secret содержит минимум 32 случайных bytes. Старые ключи требуют фиксированный
`AcceptUntil` в будущем. Cookies старого ключа перевыпускаются активным; после
deadline старый ключ отвергается. Active key тоже может иметь AcceptUntil:
после его истечения выпуск возвращает ошибку. Не продлевайте grace deadline
автоматически на restart.

Ротация двухфазная: сначала все экземпляры умеют читать новый ключ, оставляя
старый первым; затем новый становится первым, а старый получает конечный grace
deadline. После deadline старый удаляется. IDs, secrets и deadlines одинаковы
на всех экземплярах.

Stateless cookie не имеет индивидуального revocation/CAS: параллельные ответы
имеют last-cookie-wins поведение. Скопированная cookie после logout/Regenerate
воспроизводима до idle/absolute expiry или удаления ключа. Удаление cookie в
браузере не отзывает её копии. Для authentication с отзывом используйте Manager
и общий AtomicBackend. Удаление ключа отзывает все cookies этого ключа.

## CSRF: формы, JSON, login/logout

Порядок middleware: session → CSRF → auth → handler. Защищайте login/logout
так же, как другие unsafe browser-запросы. GET/HEAD/OPTIONS/TRACE считаются safe
и не должны менять бизнес-состояние. Сначала GET выдаёт случайный session-bound
token в `X-CSRF-Token`. Unsafe request передаёт один такой header (включая JSON)
либо единственный `_csrf` в теле URL-encoded/multipart формы. Имена настраиваются.

Query string, JSON field без header, duplicate headers/fields, чужой или неверный
token не проходят. Header имеет приоритет: неверный header не заменяется формой.
JSON body middleware не читает. Router заранее применяет parsing limits/cleanup.
Token responses используют Cache-Control: no-store; не переопределяйте его
публичным caching и не логируйте token. CORS не должен раскрывать token посторонним origins.

После Login/Logout/Regenerate запросите token новым GET с новой cookie: прежний
token очищен. Header token на самом transition response ещё относится к
состоянию до handler, и его нельзя использовать повторно.

## Миграция и доказательства

Это изменение pre-v1 контракта. Старые JSON snapshots и cookies без envelope v1
не загружаются: потребуется повторный вход. Старые ключи не включают поддержку
прежнего формата. При rolling deployment не смешивайте старую и новую версии
с общим namespace: смените cookie name/backend namespace либо завершите старые
экземпляры. Замените простой cache adapter на AtomicBackend, обработайте 409,
задайте TTL/Secure/ротацию и перенесите query CSRF tokens в body/header.

Проверки: [две HTTP instances](../../session/concurrency_test.go),
[CAS/revocation/TTL/keys/backend failures](../../session/lifetime_test.go),
[CSRF transports/login/logout](../../security/csrf_contract_test.go),
[streaming boundaries](../../web/writer_chain_test.go).
Две HTTP instances в тесте разделяют MemoryBackend в одном процессе: это не
подтверждает конкретный Redis/SQL adapter или межпроцессный CAS. Внешний backend
должен пройти те же сценарии и fault tests на реальном сервисе.

Основания: [OWASP Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
и [OWASP CSRF Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html).
