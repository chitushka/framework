# Cache в эксплуатации

MemoryBackend по умолчанию ограничен 10000 entries и 64 MiB значений+ключей.
NewMemoryBackendWithConfig задаёт positive MaxEntries/MaxBytes; слишком большое
значение возвращает ErrCapacity. При нехватке места удаляются expired, затем самые
старые записанные entries. Stats показывает занятость. Expiry проверяется при чтении,
периодически при записи и через PurgeExpired: приложение может вызывать его своим
таймером. Неактивный cache не запускает goroutine и удерживает не больше лимита.
Store ограничивает registry Go dependencies 10000 ключами; eviction callback означает
консервативный miss, callbacks не передаются между процессами.

Встроенные Redis/Memcached используют новые соединения на каждой операции: после
обрыва следующая операция подключается заново, неопределённая запись вслепую не
повторяется. TLSConfig клонируется; задавайте доверенный RootCAs/ServerName, проверка
сертификата включена. Redis поддерживает username/password. Memcached text protocol
не поддерживает SASL: используйте TLS с mTLS или изолированную сеть; не передавайте
ему credentials Redis. Оба backend имеют dial/I/O deadline и закрывают socket при
отмене context. Memcached keys проверяются до отправки; размеры по умолчанию ограничены
1 MiB (Memcached) и 16 MiB (Redis), настраиваются MaxValueBytes до 64 MiB.

TTL<0 отклоняется, TTL=0 означает отсутствие expiry. Redis TTL округляется до
миллисекунды, Memcached — вверх до секунды; больше 30 дней передаётся absolute Unix
time. Для переносимой логики задавайте TTL целыми секундами и синхронизируйте часы.
Namespace/tag invalidation не является транзакцией с application writes.

Store.GetOrLoad объединяет concurrent misses в пределах одного Store, до 1024
одновременных ключей. Waiter может отмениться независимо; leader context ограничивает
loader. Loader должен быть bounded/idempotent и не рекурсировать по тому же ключу.
Межпроцессный stampede control требует внешней координации или идемпотентного loader;
lock без fencing не является защитой источника истины.

Отдельный модуль `github.com/chitushka/framework/adapters/redis` использует go-redis
v9: NewClient задаёт finite budgets и отключает автоматический replay, SDK управляет
pool/reconnect. Store реализует cache Backend и атомарный CompareAndSwap с cross-key
rotation/deletion. `session.NewBackend(store)` получает требуемый AtomicBackend;
старые IDs не могут быть восстановлены stale commit. NewLimiter атомарно резервирует
account+client либо ничего, TTL ограничивает retention, IDs хэшируются. Для limiter
задавайте Redis maxmemory/noeviction и наблюдайте capacity errors; они fail closed.
Memcached и простой Get/Set/Delete не заменяют session CAS.

Доказательства: [memory/loads](../../cache/operational_test.go),
[реальные TLS/auth/outage/reconnect, session CAS и shared limiter](../../integration/cache_operational_test.go).
TLS relay в тесте проверяет проверку сертификата клиента и реальный backend за relay;
это не проверка конкретного service-side TLS deployment или отказа диска host.
