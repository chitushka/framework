# Chitu Definitive Guide

Это последовательное руководство по созданию и эксплуатации приложений на Chitu Framework.
Production readiness ещё не подтверждена: сначала прочитайте [контракт поддержки](support.md)
и [реестр гарантий с тестами и ограничениями](guarantees.md). Полный перечень методов и короткие
рецепты остаются в корневом [README](../../README.md).

## Маршрут чтения

1. [Начало работы](getting-started.md) — требования, первый HTTP-сервис и запуск.
2. [Приложение и модули](application.md) — DI, компоненты, события и жизненный цикл.
3. [Web-приложение](web.md) — маршруты, middleware, контроллеры, ответы и views.
4. [Данные](data.md) — подключения, транзакции, Query Builder, миграции и Active Record.
5. [Безопасность](security.md) — validation, sessions, authentication, CSRF и RBAC.
6. [Production](production.md) — конфигурация, cache, queues, observability и завершение работы.
7. [Качество](testing.md) — тесты, lint, CI и обязательное покрытие ядра.

Дополнительный контракт: [proxy trust, Host, redirects и CORS](proxy.md).
HTTP-семантика и сетевые доказательства: [HTTP compatibility](http-compatibility.md).
Атомарные sessions, TTL, ротация ключей и миграция: [sessions и CSRF](sessions.md).
Passwords, login throttling и token verification: [reusable authentication](authentication.md).
Production-профиль и threat-model checklist: [HTTP/tooling security audit](security-audit.md).

## Основная модель

Chitu переносит в Go удобные архитектурные идеи Yii2, но сохраняет явные зависимости и статическую типизацию:

- `app.Application` управляет модулями и остановкой;
- `di.Container` создаёт зависимости конструкторов;
- `component.Registry` хранит именованные сервисы приложения;
- `web.Router` сопоставляет запросы с handlers или controller actions;
- прикладные подсистемы подключаются обычными Go-пакетами и интерфейсами.

Начать новый проект проще всего с одного модуля и одного процесса. Делить его на отдельные сервисы следует только после появления независимых границ данных или масштабирования.

Дополнительные контракты этапов 8–9: [миграции](migrations.md), [SQL](sql-contract.md),
[Active Record](active-record.md), [генераторы](generators.md), [cache](cache.md), [очереди](queues.md).
