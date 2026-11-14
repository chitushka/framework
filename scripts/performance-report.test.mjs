import assert from "node:assert/strict"
import test from "node:test"
import { pairs, protocol, protocolHash, parseBenchmarks, summarize, compareReports, markdown } from "./performance-report.mjs"

const clone = (value) => structuredClone(value)
function text(kind, mapper = () => 100, settings = protocol.bench) {
  return pairs.filter((p) => p.kind === kind).flatMap((p) => [p.base, p.candidate].flatMap((variant) => settings.cpus.flatMap((cpu) => Array.from({ length: settings.count }, (_, i) => {
    const name = `${p.name}/${variant}`
    return `${name}${cpu === 1 ? "" : `-${cpu}`} 1000 ${mapper(name, cpu, i)} ns/op 100 B/op 4 allocs/op`
  })))).join("\n")
}
function inputs() {
  return {
    manifest: { mode: "full", protocolHash, cleanupSucceeded: true, source: { head: "a".repeat(40), cleanBefore: true, cleanAfter: true }, docker: { serverVersion: "fixture", memoryBytes: 4e9 }, host: { cpuModel: "fixture" }, serviceConfigHash: "b".repeat(64), startedUTC: "2026-11-14T00:00:00Z", finishedUTC: "2026-11-14T00:10:00Z" },
    runtime: { protocolHash, go: "go1.26.11", goos: "linux", goarch: "amd64", cpuModel: "fixture", cpuQuota: "200000 100000", memoryLimit: "1073741824", gomaxprocs: 2, cpus: 8, kernel: "fixture", services: { postgresql: "17", mysql: "8.4" } },
    goEnv: { GOVERSION: "go1.26.11", GOOS: "linux", GOARCH: "amd64", CGO_ENABLED: "1", GOFLAGS: "-p=2 -mod=readonly" },
    core: text("core"), sql: text("sql"),
    soaks: ["postgresql", "mysql"].flatMap((dialect) => ["NetHTTP", "Chitu"].map((variant) => ({
      dialect, variant, accepted: true, protocolHash, mode: "full",
      phases: protocol.soak.phases.map((p) => ({ name: p.name, scheduled: 2000, success: 2000, unavailable: 0, p99MS: 1 })),
      samples: Array.from({ length: 6 }, (_, i) => ({ phase: "recovery", seconds: 90 + i * 5 })),
      before: { heapBytes: 1, goroutines: 10 }, after: { heapBytes: 1, goroutines: 10 }, serverBefore: 1, serverAfter: 1, recoverySlopeBytesPerMinute: 0,
    }))),
  }
}

test("benchmark parser requires every case/cpu/repetition and finite units", () => {
  const raw = text("core")
  assert.equal(parseBenchmarks(raw, "core").length, 32)
  assert.equal(parseBenchmarks(raw.replaceAll("\n", "\r\n"), "core").length, 32)
  for (const bad of ["", "PASS", raw.split("\n").slice(1).join("\n"), raw + "\n" + raw.split("\n")[0], raw.replace("100 ns/op", "NaN ns/op"), raw.replace("100 ns/op", "Infinity ns/op"), raw.replace("100 B/op", "-1 B/op"), raw.replace("100 B/op", "100 ns/op"), raw.replace("4 allocs/op", "4 missing"), raw.replace(" 1000 ", " 0 "), raw.replace("/NetHTTP ", "/NetHTTP-99 ")]) {
    assert.throws(() => parseBenchmarks(bad, "core"))
  }
  assert.throws(() => parseBenchmarks(text("core", (_name, _cpu, i) => [1, 50, 100, 200, 300][i]), "core"), /noisy/)
})

test("acceptance rejects dirty, incomplete, mismatched and failed observations", () => {
  const good = inputs()
  const report = summarize(good)
  assert.equal(report.accepted, true)
  assert.match(markdown(report), /Workload/)
  for (const mutate of [
    (i) => { i.manifest.source.cleanBefore = false }, (i) => { i.manifest.source.cleanAfter = false },
    (i) => { i.manifest.cleanupSucceeded = false },
    (i) => { i.manifest.protocolHash = "wrong" }, (i) => { i.runtime.protocolHash = "wrong" },
    (i) => { i.runtime.cpuQuota = "100000 100000" }, (i) => { i.runtime.memoryLimit = "max" },
    (i) => { delete i.runtime.cpus }, (i) => { delete i.manifest.serviceConfigHash },
    (i) => { i.soaks.pop() }, (i) => { i.soaks[0].accepted = false },
    (i) => { i.soaks[0].mode = "smoke" }, (i) => { i.soaks[0].phases = [] },
    (i) => { i.soaks[0].samples = [] }, (i) => { i.soaks[0].cleanupMS = Infinity },
  ]) {
    const candidate = clone(good); mutate(candidate)
    assert.throws(() => summarize(candidate))
  }
})

test("regression budgets apply independently with complete, compatible profiles", () => {
  const baseline = summarize(inputs())
  const change = (report, metric, value) => {
    const row = report.benchmarks.core[0]; row[metric] = value
    for (const sample of row.samples) sample[metric] = value
  }
  compareReports(clone(baseline), baseline)
  for (const [metric, allowed, rejected] of [["ns", 120, 120.01], ["bytes", 110, 110.01], ["allocs", 5, 5.01]]) {
    const candidate = clone(baseline); change(candidate, metric, allowed); compareReports(candidate, baseline)
    change(candidate, metric, rejected); assert.throws(() => compareReports(candidate, baseline), /regression/)
  }
  for (const mutate of [(r) => { r.benchmarks.core = [] }, (r) => { r.benchmarks.sql[0].samples = [] }, (r) => { r.environment.profile.kernel = "different" }, (r) => { delete r.environment.profile }, (r) => { r.mode = "smoke" }, (r) => { r.benchmarks.core[0].ns = NaN }, (r) => { r.benchmarks.core[0].samples[0].iterations = 0 }]) {
    const candidate = clone(baseline); mutate(candidate); assert.throws(() => compareReports(candidate, baseline))
  }
  const empty = clone(baseline); empty.benchmarks.core = []; empty.benchmarks.sql = []
  assert.throws(() => compareReports(empty, empty), /missing/)
  const zero = clone(baseline); change(zero, "bytes", 0)
  const candidate = clone(zero); change(candidate, "bytes", 1)
  assert.throws(() => compareReports(candidate, zero), /regression/)
})
