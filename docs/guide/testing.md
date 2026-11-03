# Качество и CI

## Уровни тестирования

- Unit tests проверяют чистые правила и граничные случаи.
- Package integration tests используют реальные adapters или контролируемые fakes.
- HTTP tests собирают router и выполняют запросы через `httptest`.
- Network tests для Redis/Memcached и аналогов запускаются отдельно, когда доступна инфраструктура.

Тестируйте публичное поведение, отмену context, rollback и конкурентный доступ. Избегайте `time.Sleep`: используйте каналы, fake clock и deadlines.

## Локальные команды

```bash
make fmt-check
make vet
make test
make coverage
make lint
```

`make ci` объединяет все проверки. `lint` требует golangci-lint версии 2.13 или совместимой. Без локального Go тесты можно выполнить через Docker, как описано в корневом README.

## Политика покрытия

`scripts/check-coverage.sh` строит профиль всех библиотечных пакетов и исключает `examples`: точки входа примеров тестируются в общем `go test ./...`, но не относятся к ядру фреймворка. Взвешенное statement coverage ядра не может быть ниже 80%.

Порог — страховочная сетка, а не цель для каждого пакета. Новый код должен тестировать значимые ветви, ошибки и инварианты; бессодержательные тесты ради процента не принимаются.

## GitHub Actions

Workflow `.github/workflows/ci.yml` запускается для pull request и push в `main`/`master`. Job `test` проверяет gofmt, `go vet`, race detector, все тесты и coverage gate. Независимый job `lint` запускает официальный golangci-lint action. Coverage profile сохраняется как artifact.

Изменение обязательной проверки выполняется вместе с обновлением этого руководства, чтобы локальный и CI-контракты не расходились.
