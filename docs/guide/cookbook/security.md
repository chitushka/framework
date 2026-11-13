# Security при подключении приложения

## Browser или Bearer

Для browser login возьмите [Basic](../../../examples/basic/README.md) либо SQL
[Admin](../../../examples/advanced-admin/README.md). Порядок: session → CSRF → auth
→ handler; login/logout также требуют CSRF. После rotation заново получите token
через GET с новой cookie. Обрабатывайте 409 session conflict без слепого повтора
платёжных/других неидемпотентных действий. Для нескольких процессов нужен общий
AtomicBackend; простой CacheStore для этой гарантии недостаточен.

Для внешних access tokens используйте [REST](../../../examples/rest-api/README.md):
обязательные public key/kid/issuer/audience, allowlist алгоритмов, `iat/exp`,
ограниченный lifetime. Issuance, revocation и permission policy принадлежат
приложению/issuer. В REST starter каждый token выделенного audience имеет полный
CRUD общего каталога: не считайте JWT subject автоматической tenant isolation.

Проверяйте permission на сервере в транзакции с mutation/audit, а не только
скрывайте кнопку. Admin демонстрирует delegation ceiling и last-admin guard;
обычный RBAC Manager не добавляет эти application rules сам.

## Trust boundary и вход

1. Разрешите точные external hosts, доверяйте только CIDR настоящего proxy.
   Используйте `Context.ClientIP/Scheme/Host`, а не raw forwarded headers.
2. Примените finite body/form/upload limits до parsing. Allowlist JSON fields,
   pagination, filter/sort identifiers; ID/role/owner не должны массово назначаться.
3. Для SQL передавайте значения параметрами; dynamic identifiers берите из
   allowlist. `Raw`/`template.HTML` принимают уже доверенный контент.
4. Включите production handler снаружи публичной chain и production logger.
   Для application JSON contract на ранних server errors используйте
   `web.WithServerHandlerWrapper`, как в REST starter; wrapper не читает body.
5. Cookies: Secure/HttpOnly/SameSite, credentials limiter и конечные TTL. CORS
   credentials разрешайте только выбранным origins, не wildcard. CSRF и CORS
   решают разные границы и не заменяют серверную authorization.

Проверьте missing/expired/tampered credentials, forged X-Forwarded-For, неизвестные
JSON fields, oversized body, чужой CSRF и прямую HTTP mutation без permission.
Готовые [REST contracts](../../../examples/rest-api/contract_test.go) и
[Admin SQL tests](../../../examples/advanced-admin/integration_test.go) показывают
такие проверки; они не являются аудитом произвольного нового приложения.
Все несовместимые изменения сведены в [migration guide](../api-migration.md),
threat-model и ограничения redaction/files — в [security audit](../security-audit.md).
