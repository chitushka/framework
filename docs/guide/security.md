# Безопасность

## Validation и mass assignment

Сценарий validation определяет правила конкретной операции (`create`, `update`). `Scenario.Fields` — allowlist массового присваивания. Никогда не разрешайте клиенту назначать ID, роль, owner или системные timestamps только потому, что поле присутствует в JSON.

Сначала конвертируются все допустимые значения, затем изменяется модель: ошибочный payload не оставляет частично обновлённый объект.

## Sessions и authentication

Session backend должен быть общим для всех экземпляров приложения. Memory backend подходит только тестам и локальному запуску. Cookie должна иметь `Secure`, `HttpOnly` и подходящий `SameSite`; TLS завершается на доверенном proxy или в приложении.

`auth.Middleware` устанавливается после session middleware. `Login` и `Logout` регенерируют session ID, защищая от fixation. Identity loader не должен возвращать отключённых пользователей.

## CSRF и headers

CSRF middleware обязателен для state-changing browser-запросов с cookie authentication. Bearer API, не использующий cookie, обычно защищается проверкой токена и CORS-политикой вместо CSRF. Security headers включайте централизованно и адаптируйте CSP под реальные assets.

## RBAC

Разрешения называют бизнес-действия (`post.update`), роли группируют их. Проверяйте permission, а не имя роли, в HTTP-слое. Контекстные rules получают только необходимые параметры, например owner ID.

Изменения ролей и назначений должны аудироваться. Для нескольких процессов используйте `rbac.DatabaseStore`; memory store предназначен для тестов и примеров.

## Secrets

Secrets приходят из окружения или secret manager, не из репозитория. Задавайте минимальную длину ключа, сроки токенов и план ротации. Не логируйте passwords, session cookies, authorization headers и полные connection strings.
