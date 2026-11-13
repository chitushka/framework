.PHONY: fmt fmt-check test vet lint coverage ci integration basic basic-build basic-e2e admin admin-build admin-e2e rest rest-e2e docs docs-test security frontend coverage-test

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.cache/*' -not -path './frontend/node_modules/*')

fmt-check:
	@files="$$(find . -name '*.go' -not -path './.cache/*' -not -path './frontend/node_modules/*' -print0 | xargs -0 gofmt -l)"; test -z "$$files" || (echo "$$files" && exit 1)

test:
	go test ./...
	go -C adapters test ./...
	go -C examples/advanced-admin test ./...
	go -C integration test -run '^$$' ./...

vet:
	go vet ./...
	go -C adapters vet ./...
	go -C examples/advanced-admin vet ./...
	go -C integration vet ./...

lint:
	golangci-lint run --build-tags=e2e ./...
	cd adapters && golangci-lint run
	cd integration && golangci-lint run
	cd examples/advanced-admin && golangci-lint run --build-tags=integration

coverage-test:
	sh ./scripts/test-coverage.sh

frontend:
	cd frontend && npm ci --no-audit --no-fund && npm run lint && npm run build && npm test
	node --test scripts/*.test.mjs
	node scripts/npm-audit.mjs

security:
	sh ./scripts/security.sh

coverage:
	sh ./scripts/check-coverage.sh coverage.out 80

ci: fmt-check vet test docs-test coverage-test coverage lint frontend security

docs:
	go run ./tools/checkdocs

docs-test:
	go run ./tools/checkdocs -run

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
