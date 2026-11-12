# Lifecycle, health и shutdown budget

`Application` — одно поколение процесса: повторный Start после успеха, ошибки или
rollback запрещён. Сначала Register всех modules, затем Boot в порядке добавления.
Running/readiness становятся true после успешного EventAfterStart. Ошибка Register,
Boot или start hook оставляет readiness false и откатывает успешно запущенные modules.
Модуль, не завершивший Boot успешно, сам отвечает за свои частично созданные ресурсы.

## Общий бюджет

```go
application := app.New(nil, nil)
if err := application.SetShutdownTimeout(12 * time.Second); err != nil {
    return err
}
ctx, stop := app.SignalContext(context.Background()) // interrupt и SIGTERM
defer stop()
return application.Run(ctx)
```

DefaultShutdownTimeout = 15s; положительное значение устанавливается до Start.
Run при отмене или ErrorSource запускает cleanup с независимым context, сохраняющим
values исходного context. Уже отменённый Boot также получает независимый rollback
deadline. Отмена контекста запуска не отнимает время у cleanup.

Shutdown выполняется один раз; конкурентные/повторные вызовы ждут общий done и
возвращают сохранённую ошибку. Отмена ожидающего caller прекращает его ожидание,
cleanup продолжает работать. Deadline первого caller может сократить общий budget;
повторный caller не перезапускает и не продлевает его. Run также возвращается при
внешнем Shutdown, даже если его собственный context не отменён.

Порядок остановки:

1. Состояние становится stopping: Running/readiness = false, Live = true.
2. EventBeforeStop (здесь можно выдержать короткое время распространения readiness).
3. Optional `app.Quiescer.Quiesce` всех запущенных modules в обратном порядке.
   Worker прекращает reserve, оставляя активным jobs время на завершение.
4. Shutdown modules в обратном порядке, затем EventAfterStop.
5. Сохраняется объединённая ошибка, закрывается общий done, Live = false.

Hooks, quiesce, HTTP/worker drain и finalizers делят **один** deadline. Локальные
timeouts не продлевают его. Ошибка одного module не пропускает остальные cleanup
callbacks. При исчерпании budget они вызываются с истёкшим context; ошибки содержат
имя этапа/module, а `errors.Is(err, context.DeadlineExceeded)` сохраняется.
Cleanup panic преобразуется в ошибку. Stop hooks выполняются и при boot rollback.

Сначала добавляйте cleanup modules ресурсов/exporter, затем workers, затем публичный
HTTP server: так HTTP завершится до worker drain, exporter — после обоих.

```go
exporterModule, err := app.NewFinalizer("telemetry", provider.Shutdown)
if err != nil { return err }
if err := application.AddModule(exporterModule); err != nil { return err }
if err := application.AddModule(worker); err != nil { return err }
if err := application.AddModule(server); err != nil { return err }
```

NewFinalizer ничего не создаёт в Register/Boot. До успешного Boot finalizer caller
владеет ресурсом: если запуск не дошёл до него, ресурс нужно закрыть самостоятельно.
Соединения, нужные exporter/worker, должны закрываться ещё позже. Grace period
платформы должен превышать общий budget с запасом на выход runtime. Ошибки Run
нужно логировать и возвращать как неуспешное завершение процесса.

## Liveness и readiness

Endpoints подключаются явно, предпочтительно на закрытый management listener.
`WithServerID("management")` позволяет зарегистрировать второй web.Server;
его component имеет имя `web.server.management`, стандартный — `web.server`.
Management module добавляют перед worker/traffic server, чтобы probes были доступны
во время drain. Ничего не публикуется автоматически на бизнес-маршрутах.

```go
health, err := web.NewHealth(application, web.HealthConfig{
    ProbeTimeout:        200 * time.Millisecond,
    MaxConcurrentProbes: 4,
    Checks: []func(context.Context) error{connection.DB().PingContext},
})
if err != nil { return err }
mux := http.NewServeMux()
mux.HandleFunc("/live", health.Liveness)
mux.HandleFunc("/ready", health.Readiness)
```

| Состояние | Liveness | Readiness |
| --- | --- | --- |
| До/во время Start | 200 | 503 |
| Running, обязательные dependencies доступны | 200 | 200 |
| Dependency недоступна, timeout/overload/panic probe | 200 | 503 |
| Stopping / drain | 200 | 503 |
| Stopped / завершённый rollback | 503 | 503 |

GET и HEAD поддерживаются; другие методы дают 405/Allow. Responses имеют no-store
и общий текст без DSN/ошибок dependencies. Liveness не обращается к зависимостям.
Checks должны быть безопасными read-only операциями, проверять только обязательные
dependencies и учитывать context. По умолчанию **весь** readiness request имеет 250ms,
а один Health допускает максимум четыре одновременно исполняемых probe callbacks.
Checks выполняются последовательно под общим deadline; после них Running проверяется
повторно. Допускается до 32 checks и 64 concurrent callbacks при явной настройке.
При заполнении глобальных slots readiness немедленно возвращает 503. Callback,
игнорирующий cancellation, удерживает slot до своего фактического завершения: новые
HTTP probes не создают неограниченное число зависших goroutines.

## Ограничения и миграция

Go не может принудительно уничтожить goroutine. Framework ограничивает ожидание
Register/Boot/start hooks при отмене context и всех cleanup callbacks по deadline,
но не доказывает завершение кода, игнорирующего cancellation. Каждый зависший callback
может оставаться до выхода процесса; callbacks, запущенные после общего deadline,
могут пересекаться. После такого timeout требуется выход процесса. Не переиспользуйте
его modules и ресурсы. Размер этой работы ограничен количеством зарегистрированных
modules/hooks; readiness probes дополнительно ограничены slots.

Startup deadline задаёт caller Start/Run; без отмены context медленный Boot может
работать сколько угодно. Shutdown во время Start отменяет startup и ждёт bounded
rollback; провалившийся/зависший Boot не включается в список успешно запущенных modules.
Register не должен захватывать ресурсы, требующие rollback. Lifecycle hooks не должны
синхронно вызывать Shutdown своего Application, иначе будут ждать собственный done
до deadline. Для остановки из hook отменяйте управляющий context.

HTTP по локальному/общему deadline вызывает Close и отменяет request contexts.
Hijacked connections принадлежат приложению и требуют своего drain. Workers и
exporters должны соблюдать context; незавершённые jobs остаются subject to broker
lease/reclaim и at-least-once delivery. Дедлайн не означает успешный Ack/flush.

Изменения совместимости: общий finite budget вместо неограниченного ожидания,
общий результат повторного Shutdown, stop hooks при rollback, SignalContext c SIGTERM,
quiesce workers до reverse drain, отдельные component names нестандартных servers.
Собственный `signal.NotifyContext(..., os.Interrupt)` следует заменить SignalContext
или явно добавить SIGTERM. Настройки lease/grace/exporter timeout согласуйте с budget.

Доказательства: [unit lifecycle](../../app/lifecycle_test.go),
[health concurrency/privacy](../../web/health_test.go),
[HTTP close/repeated shutdown](../../web/server_lifecycle_test.go),
[Linux SIGTERM + PostgreSQL/Redis/collector](../../integration/lifecycle_process_test.go).
Полный real-service suite запускается через `scripts/integration.ps1` или
`sh scripts/integration.sh`, включая четыре process cases: graceful, hung HTTP,
hung worker и hung exporter. Process test проверяет 2s cleanup budget с общим пределом
5s с учётом race runtime, readiness 503 до остановки admission, Ack текущей задачи,
сохранность ещё не зарезервированной задачи и export законченного HTTP span.
