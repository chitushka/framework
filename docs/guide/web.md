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

## Middleware

Middleware применяется в порядке регистрации и должно либо вызвать `next`, либо вернуть ответ/ошибку. Рекомендуемый внешний порядок: recovery, request ID/logging, security headers, session, authentication, authorization, handler.

Middleware не должно сохранять request-specific данные в полях общего объекта без синхронизации. Используйте контекст запроса.

## Controllers и actions

Для небольшого endpoint достаточно `web.Handler`. Controller удобен, когда actions используют общие hooks. Зависимости action разрешаются через DI на каждый запрос; `BeforeAction` может отменить вызов, `AfterAction` видит его ошибку.

## Ввод и ответы

`BindJSON` принимает ровно одно JSON-значение (в том числе массив или scalar, если destination
это допускает) и отклоняет неизвестные поля struct. Размер body и Content-Type policy
автоматически не ограничиваются. После декодирования выполняйте validation до изменения постоянного состояния.

Используйте специализированные методы `HTML`, `JSON`, `XML`, `File`, `Download`, `Stream` и `NoContent`.
Они отслеживают начало ответа и задают content type; прямая запись через ResponseWriter обходит
часть этих проверок, а ошибка после начала stream не может отменить отправленные байты.
Публичные сообщения об ошибках задавайте через `web.NewHTTPError`; внутреннюю причину передавайте
третьим аргументом для логирования и `errors.Is`. Полная сетевая совместимость writer wrappers
остаётся задачей этапа 7.3.

## Views и frontend

`web.Renderer` читает templates из `fs.FS`, поддерживает layouts и widgets. Данные для Vue сериализуются через `vueState`, что защищает HTML-контекст. Assets регистрируются через `asset.Registry`; production bundle строится из Vite manifest, development bundle подключает HMR server.

Не вставляйте пользовательские строки в `template.HTML`. Статические файлы отдавайте отдельным handler или reverse proxy.
