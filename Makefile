.PHONY: fmt fmt-check test vet lint coverage ci integration basic basic-build basic-e2e admin admin-build admin-e2e rest rest-e2e

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.cache/*' -not -path './frontend/node_modules/*')

fmt-check:
	@files="$$(find . -name '*.go' -not -path './.cache/*' -not -path './frontend/node_modules/*' -print0 | xargs -0 gofmt -l)"; test -z "$$files" || (echo "$$files" && exit 1)

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

coverage:
	sh ./scripts/check-coverage.sh coverage.out 80

ci: fmt-check vet test coverage lint

integration:
	sh ./scripts/integration.sh

basic:
	node scripts/basic.mjs start

basic-build:
	node scripts/basic.mjs build

basic-e2e:
	sh ./scripts/basic-e2e.sh

admin:
	node scripts/admin.mjs start

admin-build:
	node scripts/admin.mjs build

admin-e2e:
	sh ./scripts/admin-e2e.sh

rest:
	go run ./examples/rest-api

rest-e2e:
	go test -race -tags e2e -count=1 ./examples/rest-api
