# Reusable authentication

`auth` предоставляет password hashing, проверку credentials с throttling и
bearer token verification. Он не хранит пользователей за приложение, не выдаёт
access/refresh tokens и не является identity provider. Приложение задаёт password
policy, canonical login, identity persistence, permissions, MFA и revocation.
Минимальный Go — 1.26.11: проверка шага 10.1 обнаружила GO-2026-6859 в net/http
предыдущего 1.26.10. Dependency versions и checksums закреплены в go.mod/go.sum;
обновление auth не отменяет необходимость регулярного security scan toolchain.
Проверка шага 7.5 govulncheck v1.9.0 на Go 1.26.10 на тот момент не находила уязвимостей в используемых
packages/symbols. Module-only advisory GO-2026-5932 относится к неиспользуемому
`x/crypto/openpgp`; framework импортирует только поддерживаемый Argon2id,
OpenPGP не используется. Это сообщение не подавляется конфигурацией сканера.

## PasswordHasher

`NewPasswordHasher(PasswordConfig{})` использует Argon2id из `golang.org/x/crypto`:
19 MiB, две итерации, один lane, 16-byte random salt и 32-byte key. Одновременно
выполняются максимум две операции на экземпляр hasher. Переиспользуйте один hasher
на приложение; не создавайте его на каждый запрос. Параметры следует измерить на
целевом hardware и увеличить с учётом login load и memory budget.

```go
hasher, err := auth.NewPasswordHasher(auth.PasswordConfig{})
if err != nil { return err }
encoded, err := hasher.Hash(ctx, password)
if err != nil { return err }
needsRehash, err := hasher.Verify(ctx, password, encoded)
```

Hash возвращает PHC `$argon2id$v=19$m=...,t=...,p=...$salt$key`.
Verify проверяет формат, costs и decoded lengths до KDF, затем сравнивает key в
constant time. Поддерживаются 8–64 MiB, 1–5 итераций и 1–4 lanes; нижние costs
нужны для контролируемой миграции, default выше. Salt/key имеют фиксированные
размеры, encoded hash ограничен 256 bytes. Неподдерживаемые legacy formats
(включая bcrypt) требуют отдельного migration adapter, не угадываются автоматически.

Пароль не обрезается, не trim/normalize-ится: принимаются 1–1024 bytes.
Минимальную длину, breached-password checks и правила создания паролей задаёт
приложение. Максимум concurrency настраивается от 1 до 32; лимит памяти одного
hasher не превышает concurrent slots × 64 MiB плюс overhead. Ожидание slot
учитывает context cancellation. Уже начатый Argon2 вызов нельзя прервать:
ошибка отмены вернётся после него, максимум costs ограничен заранее.

Verify возвращает `ErrInvalidCredentials` для несовпадения и
`ErrInvalidPasswordHash` для неверного формата/cost. После успешной проверки
needsRehash=true, если целевые memory/iterations выше и ни один не ниже старых.
Автоматического downgrade или перенастройки только lanes нет. Конфликтующие
cost policies требуют отдельной миграции. Hash не хранит plaintext password.

## Credentials и login throttling

`NewPasswordAuthenticator(store, hasher, limiter)` требует все три зависимости.
`CredentialStore.FindCredential` получает canonical login и возвращает
`Credential{Identity, PasswordHash, Disabled}`; отсутствующий пользователь — nil,nil.
`ReplacePasswordHash` обязан атомарно сравнить old hash: rehash не может
перезаписать параллельный password reset. Backend error возвращается вызывающему
коду; CAS conflict при rehash отклоняет текущий вход.

`Authenticate(ctx, login, password, clientKey)` сначала резервирует попытку в
AttemptLimiter, затем обращается к store и проверяет пароль. Canonical login
формирует приложение одинаково для lookup и throttling (case/Unicode aliases
не должны обходить account limit). Client key обязателен: используйте IP из
проверенной proxy policy, а не произвольный X-Forwarded-For или поле формы.
Unknown/disabled accounts и неверный password возвращают одинаковый
`ErrInvalidCredentials`. Для отсутствующего/disabled/corrupt hash выполняется
dummy KDF со случайным секретом текущего процесса. Это не обещание одинакового
полного timing: DB latency и costs старых hashes могут отличаться.

После успеха browser handler вызывает `auth.Must(ctx.Request.Context()).Login(identity)` под
session → CSRF → auth middleware; алгоритм sessions описан [отдельно](sessions.md).
Registration/password reset используют Hash и собственную policy/verification.
Session/token invalidation при password reset и отключении account — ответственность
приложения. Authenticator не создаёт сессий или cookies автоматически.

`MemoryAttemptLimiter` использует общие для его экземпляра account/client counters:
default 5 попыток/account и 30/client за минуту, максимум 10000 entries.
Попытка резервирует оба счётчика атомарно, включая одновременные запросы. Успех
также учитывается, reset после успеха отсутствует. Fixed windows допускают burst
около границы окна. Keys хранятся как SHA-256 с разделением account/client domains;
это не гарантия анонимности PII. Expired entries освобождаются; при заполнении
новые keys отклоняются с `ErrLimiterCapacity`, вытеснения активных counters нет.
Такой fail-closed предел позволяет DoS при атаке на множество login names: нужны
внешние IP/global limits и monitoring, размеры подбираются по нагрузке.

Limiter process-local: для нескольких процессов приложение предоставляет общий
`AttemptLimiter` с атомарным reserve обоих counters. Backend failure не должен
превращаться в allow. Положительный retryAfter означает отказ без списания
попыток. Authenticator возвращает `ThrottleError` с RetryAfter и
`errors.Is(err, auth.ErrThrottled)`. HTTP handler отображает его в 429 и
Retry-After (целые секунды с округлением вверх); invalid credentials — в общий
401; backend/capacity error — в общий 503/500. Пароли и backend details не
выдавайте в error body. Используйте finite contexts и request limits до binding.

## JWTVerifier и Bearer

JWT проверяет `github.com/golang-jwt/jwt/v5`, а не самописный JWT parser.
`NewJWTVerifier` требует issuer, audience, явный allowlist алгоритмов и 1–16
trusted public keys. Поддерживаются EdDSA (Ed25519) и RS256 (RSA 2048–8192 bits).
`none`, HMAC и другие algorithms не принимаются; каждый kid привязан к одному
algorithm/key type. Signing/private keys verifier не нужны.

```go
verifier, err := auth.NewJWTVerifier(auth.JWTConfig{
    Issuer:     "https://identity.example",
    Audience:   "articles-api",
    Algorithms: []string{"EdDSA"},
}, []auth.JWTKey{{ID: keyID, Algorithm: "EdDSA", PublicKey: publicKey}})
if err != nil { return err }
if err := router.Use(auth.Bearer(verifier)); err != nil { return err }
```

Обязательны signature, kid, точный typ (default `at+jwt`), issuer, audience,
непустой subject, exp и iat. При наличии nbf проверяется. Future iat, expired exp,
exp≤iat и lifetime больше MaxLifetime отклоняются. Default MaxLifetime=15 минут,
максимум 24 часа; Leeway default=0, максимум 2 минуты. Если provider использует
typ=JWT, это нужно явно указать в config. Token ограничен 8192 bytes.
Headers crit/jku/jwk/x5u/b64 отвергаются: никакой загрузки ключей по данным token
не производится. Это не JWKS client; внешний key refresh делает доверенный
компонент приложения с собственной TLS/timeout/cache policy.

`ReplaceKeys` валидирует полный snapshot и атомарно заменяет key set, копируя
public keys. Неверное обновление сохраняет старый snapshot. Ротация: сначала
распространите old+new keys всем verifiers, затем переключите issuer на новый kid;
после максимального TTL+leeway удалите старый ключ или ограничьте его AcceptUntil.
AcceptUntil проверяется без leeway. ReplaceKeys(nil) отзывает весь набор. Запрос,
который уже загрузил старый snapshot, может закончить проверку по нему.

`Bearer` принимает ровно один Authorization header; cookie/query tokens
игнорируются. Нет/неверный token — общий 401 с WWW-Authenticate; verifier/backend
failure — общий 503. Ошибки signature/keys/claims не раскрываются. Responses
используют Cache-Control: no-store. `PrincipalFromContext` возвращает проверенные
Subject/Issuer/Audience/IssuedAt/ExpiresAt/TokenID. Эти поля не заменяют permissions,
disabled-user checks или token revocation: приложение проверяет их отдельно.
Другие провайдеры могут реализовать `TokenVerifier`; контракт требует всей
проверки authenticity/intended recipient/lifetime до возврата principal.

## Starters, миграция и доказательства

REST-пример теперь только проверяет внешние Ed25519 access tokens: `/v1/token`,
client credentials, HS256 и JWT_SECRET/fallback удалены. Нужны
JWT_PUBLIC_KEY_FILE, JWT_KEY_ID, JWT_ISSUER, JWT_AUDIENCE и внешний issuer с
typ=at+jwt, iat/exp и lifetime≤15 минут. [Запуск](../../examples/README.md).
Basic использует PasswordAuthenticator, native browser forms с CSRF/validation,
bounded sessions/limiter и явный production PHC/host config; [Basic guide](../../examples/basic/README.md)
описывает HTTP/HTTPS E2E и ограничения одного процесса. [Advanced Admin](../../examples/advanced-admin/README.md)
использует SQL CredentialStore/rehash, persisted auth epoch, password login и bounded
sessions/limiter. Сброс пароля, disable/enable и SQL revoke проверяются при каждом
защищённом запросе; данные переживают restart, sessions остаются process-local.
[Приёмка 11.2](../stage-11-2-acceptance.md) содержит SQL и browser evidence.
Production-ready v1.0 всего framework этим не объявляется.

Проверки: [password format/costs/cancellation](../../auth/password_test.go),
[credentials/rehash/errors/concurrent throttle](../../auth/credentials_test.go),
[JWT negative cases/rotation/race](../../auth/jwt_test.go),
[bearer error/transport boundaries](../../auth/token_test.go),
[REST](../../examples/rest-api/main_test.go).
Внешний CredentialStore/AttemptLimiter и issuer требуют своих integration/outage
tests. MFA, refresh tokens, OAuth/OIDC discovery и token issuance здесь не реализованы.

Основания: [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html),
[Argon2](https://pkg.go.dev/golang.org/x/crypto/argon2),
[golang-jwt parser/validator](https://pkg.go.dev/github.com/golang-jwt/jwt/v5).
