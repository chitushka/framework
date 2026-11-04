# Безопасность

## Validation и mass assignment

Сценарий validation определяет правила конкретной операции (`create`, `update`). `Scenario.Fields` — allowlist массового присваивания. Никогда не разрешайте клиенту назначать ID, роль, owner или системные timestamps только потому, что поле присутствует в JSON.

Сначала конвертируются все допустимые значения, затем изменяется модель: ошибочный payload не оставляет частично обновлённый объект.

## Sessions и authentication

Session backend должен быть общим для всех экземпляров приложения. Memory backend подходит только тестам и локальному запуску. Cookie должна иметь `Secure`, `HttpOnly` и подходящий `SameSite`; TLS завершается на доверенном proxy или в приложении.

`auth.Middleware` устанавливается после session middleware. `Login` и `Logout` регенерируют
session ID. AtomicBackend предотвращает перезапись конфликтующего снимка и восстановление
отозванного ID; конфликт возвращает 409. Idle/absolute TTL и bounded cookie key rotation
описаны в [контракте sessions/CSRF и инструкции миграции](sessions.md).
Identity loader не должен возвращать отключённых пользователей. Argon2id hashing/rehash,
credential verification с throttling и JWT/bearer verifier предоставляет пакет auth;
[контракт и подключение](authentication.md). User persistence, общий limiter для нескольких
процессов, MFA, permissions и revocation policy предоставляет приложение.

`CookieStore` stateless: старую зашифрованную cookie нельзя отозвать на сервере,
проверяются серверные idle/absolute TTL, доступна ротация ключей. Для требований revocation выбирайте
server-side storage и проверяйте его ограничения из [реестра гарантий](guarantees.md).

## CSRF и headers

CSRF middleware обязателен для state-changing browser-запросов с cookie authentication. Bearer API, не использующий cookie, обычно защищается проверкой токена и CORS-политикой вместо CSRF. Security headers включайте централизованно и адаптируйте CSP под реальные assets.

## RBAC

Разрешения называют бизнес-действия (`post.update`), роли группируют их. Проверяйте permission, а не имя роли, в HTTP-слое. Контекстные rules получают только необходимые параметры, например owner ID.

Изменения ролей и назначений должны аудироваться. Для нескольких процессов используйте `rbac.DatabaseStore`; memory store предназначен для тестов и примеров.

## Secrets

Production-профиль, redaction, rooted files и проверенные HTTP/tooling границы
описаны в [security audit checklist](security-audit.md).

Secrets приходят из окружения или secret manager, не из репозитория. Задавайте минимальную длину ключа, сроки токенов и план ротации. Не логируйте passwords, session cookies, authorization headers и полные connection strings.
