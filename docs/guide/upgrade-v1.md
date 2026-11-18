# Обновление принятого RC до v1.0.0

Это план обновления **после разрешённой публикации v1.0.0**. Сейчас v1.0.0 —
локальный unpublished draft. Исходный принятый объект — v0.1.0-rc.1,
`90b2c3bf936c5d7f67eb2a4b9216d438b1f40739`. [Freeze](stability.md) и
[checklist](../release-checklist-v1.md) определяют условия выпуска.

## RC1 → v1.0.0

Публичные Go signatures и runtime/external dependency bytes сохраняются.
Меняются собственные module version pins и metadata поставки. Для уже принятого
RC1 **не требуется новый session namespace, queue type или migration history
schema только из-за v1.0 label**. Не запускайте `resolve`/DDL, если нет dirty
состояния и отдельно проверенной application migration.

После публикации root и вложенных tags используйте совместимые версии:

```bash
go get github.com/chitushka/framework@v1.0.0
go get github.com/chitushka/framework/adapters/redis@v1.0.0
go mod verify
go test ./...
```

Root path остаётся `github.com/chitushka/framework`, без `/v1`. Nested tags:
`adapters/v1.0.0`, `examples/advanced-admin/v1.0.0`. Внешний module не наследует
workspace replaces. Admin остаётся source scaffold; используйте `go get` и
`go build` внутри своего module, как описано в [release policy](releases.md).

Перед rollout:

1. Закрепите source/target versions и digests. Проверьте SHA256SUMS, SPDX,
   provenance и notices; unsigned local evidence не удостоверяет автора.
2. Сохраните DB и persistent DLQ, проверьте restore на отдельной БД. Сохраните
   старые migration sources/checksums и public keys на период rollback.
3. Выполните consumer tests, SQL/cache/queue и application authorization tests.
   Согласуйте grace/lease/job/HTTP/export deadlines; не вводите infinite retries.
4. Для compatible application schema используйте expand → новые replicas →
   readiness → drain старых → отложенный contract. Session CAS/409 и business
   side effects не образуют одну transaction: повторяйте только idempotent work.
5. При JWT rotation сначала overlap с конечным AcceptUntil, затем новый issuer
   kid, затем revoke old. TLS reload проверяйте клиентом с CA/hostname verification.
6. Наблюдайте error rate, latency, backlog, DLQ, export failures и ресурсы.
   Верните прежние image/config при нарушении SLO. Не откатывайте schema поверх
   данных новых writers; restore — отдельная проверенная операторская процедура.

## Более ранние 0.x deployments

Прямой zero-downtime upgrade с произвольного раннего commit не обещается.
Используйте [pre-v1 migration guide](api-migration.md): AtomicBackend/session v1,
CSRF form/header transport, password/JWT вместо demo issuer, durable DLQ,
quorum AMQP names, nullable SQL trace columns, bounded tracer/lifecycle.
Старые classic queues нельзя превратить в quorum простой redeclare.

Если смешанные версии storage несовместимы, остановите writers/consumers,
перенесите сообщения и завершите старые sessions по своему migration plan.
При неизвестном исходе SQL/HTTP commit сначала сверяйте состояние и operation ID;
retry не должен дублировать бизнес-effect. MemoryBroker/MemoryBackend и
process-local starter state не станут persistent после обновления dependency.

Перечень проверенных публичных контрактов — [consumer fixtures](../../compatibility/public_test.go).
Они компилируются и исполняются в отдельном модуле установленного RC и stable draft;
не заменяют migrations и tests бизнес-приложения.
