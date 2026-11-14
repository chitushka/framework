# Contributing

Изменения собственного кода проекта принимаются под [MIT](LICENSE). Сохраняйте
copyright и license notices стороннего кода. Новая зависимость требует обновить
lock/checksum files, license inventory и соответствующий SBOM; precompiled
frontend assets требуют повторного аудита `release/frontend-audit.json`.

Начните с [README](README.md), [границ поддержки](docs/guide/support.md)
и [PLAN](PLAN.md). Обсудите изменение публичного API до его реализации. PR должен
описывать воспроизводимую проблему, итоговое поведение и выполненные проверки.
Для исправления дефекта добавьте regression test на наблюдаемое поведение.
Не добавляйте secrets, generated public assets, node_modules или local caches.

Перед PR выполните `make ci`, затем затронутые real-service/E2E проверки из
Makefile. Docker необходим для SQL/cache/queue и контейнерной приёмки. Обязательные
jobs и aggregate gate перечислены в `.github/workflows/ci.yml`; skipped/failing
обязательный job не считается успешной проверкой. Release-проверки описаны в
[release policy](docs/guide/releases.md).

Уязвимости направляйте приватно по [SECURITY.md](SECURITY.md). Не публикуйте
exploit, действующие credentials и персональные данные в issue или PR.
