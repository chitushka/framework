#!/usr/bin/env sh
set -eu
go env -json GOVERSION GOOS GOARCH CGO_ENABLED GOFLAGS > "$PERF_OUTPUT_DIR/go-env.json"
go test ./internal/performance
go -C integration vet -tags performance ./performance
go -C integration test -tags performance -run '^TestPerformanceMetadata$' -count=1 -timeout=1m ./performance
go test ./internal/performance -run '^$' -bench '^Benchmark(Routing|DI|Rendering|SQLBuild)$' -benchmem -benchtime "$PERF_BENCH_TIME" -count "$PERF_BENCH_COUNT" -cpu "$PERF_BENCH_CPUS" > "$PERF_OUTPUT_DIR/core.txt"
go -C integration test ./performance -tags performance -run '^$' -bench '^BenchmarkSQL$' -benchmem -benchtime "$PERF_BENCH_TIME" -count "$PERF_BENCH_COUNT" -cpu "$PERF_BENCH_CPUS" -timeout=5m > "$PERF_OUTPUT_DIR/sql.txt"
go -C integration test ./performance -tags performance -run '^TestPerformanceSoak$' -count=1 -timeout=12m -v > "$PERF_OUTPUT_DIR/soak.log" 2>&1
