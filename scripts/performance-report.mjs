import { readFileSync, writeFileSync } from "node:fs"
import { createHash } from "node:crypto"
import { join, resolve } from "node:path"
import { pathToFileURL } from "node:url"

export const protocolBytes = readFileSync(new URL("../internal/performance/protocol.json", import.meta.url), "utf8").replaceAll("\r\n", "\n")
export const protocol = JSON.parse(protocolBytes)
export const protocolHash = createHash("sha256").update(protocolBytes).digest("hex")
export const pairs = [
  ...protocol.bench.routes.map((n) => ({ kind: "core", name: `BenchmarkRouting/Routes${n}`, base: "NetHTTP", candidate: "Chitu" })),
  ...["Transient", "Singleton"].map((n) => ({ kind: "core", name: `BenchmarkDI/${n}`, base: "Manual", candidate: "Container" })),
  { kind: "core", name: "BenchmarkRendering", base: "Template", candidate: "Renderer" },
  ...["postgresql", "mysql"].map((n) => ({ kind: "core", name: `BenchmarkSQLBuild/${n}`, base: "Static", candidate: "Builder" })),
  ...["postgresql", "mysql"].map((n) => ({ kind: "sql", name: `BenchmarkSQL/${n}`, base: "NetSQL", candidate: "Chitu" })),
]

export function median(values) {
  const sorted = [...values].sort((a, b) => a - b)
  const i = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[i] : (sorted[i - 1] + sorted[i]) / 2
}

export function parseBenchmarks(text, kind, settings = protocol.bench) {
  const expected = new Set(pairs.filter((p) => p.kind === kind).flatMap((p) => [p.base, p.candidate].map((v) => `${p.name}/${v}`)))
  if (!expected.size) throw new Error("unknown benchmark suite")
  const groups = new Map()
  for (const line of text.split(/\r?\n/)) {
    if (!line.startsWith("Benchmark")) continue
    const [label, iterations, ...cells] = line.trim().split(/\s+/)
    const match = /^(.*?)(?:-(\d+))?$/.exec(label)
    const name = match[1], cpu = Number(match[2] ?? 1)
    if (!expected.has(name) || !settings.cpus.includes(cpu) || !Number.isSafeInteger(Number(iterations)) || Number(iterations) <= 0 || cells.length !== 6) throw new Error(`invalid benchmark row: ${line}`)
    const metrics = {}
    for (let i = 0; i < cells.length; i += 2) {
      const value = Number(cells[i]), unit = cells[i + 1]
      if (!["ns/op", "B/op", "allocs/op"].includes(unit) || unit in metrics || !Number.isFinite(value) || value < 0) throw new Error(`invalid benchmark metric: ${line}`)
      metrics[unit] = value
    }
    if (!(metrics["ns/op"] > 0) || !["B/op", "allocs/op"].every((unit) => unit in metrics)) throw new Error(`missing benchmark units: ${line}`)
    const key = `${name}@${cpu}`
    const group = groups.get(key) ?? { name, cpu, samples: [] }
    group.samples.push({ iterations: Number(iterations), ns: metrics["ns/op"], bytes: metrics["B/op"], allocs: metrics["allocs/op"] })
    groups.set(key, group)
  }
  const results = []
  for (const name of expected) for (const cpu of settings.cpus) {
    const group = groups.get(`${name}@${cpu}`)
    if (group?.samples.length !== settings.count) throw new Error(`missing/repeated benchmark samples: ${name}@${cpu}`)
    for (const metric of ["ns", "bytes", "allocs"]) group[metric] = median(group.samples.map((s) => s[metric]))
    group.madPercent = 100 * median(group.samples.map((s) => Math.abs(s.ns - group.ns))) / group.ns
    if (group.madPercent > protocol.regression.maxMADPercent) throw new Error(`noisy benchmark: ${name}@${cpu} MAD=${group.madPercent.toFixed(1)}%`)
    results.push(group)
  }
  return results
}

export function compareReports(candidate, baseline) {
  for (const report of [candidate, baseline]) {
    finiteNumbers(report)
    if (report.mode !== "full" || report.protocolHash !== protocolHash || report.source?.cleanBefore !== true || report.source?.cleanAfter !== true || !report.environment?.profile?.cpuModel || !report.environment.profile.go || !report.environment.profile.kernel || !report.environment.profile.serviceConfigHash || !report.environment.profile.goEnv) throw new Error("incomplete comparison report")
    for (const kind of ["core", "sql"]) {
      const expected = pairs.filter((p) => p.kind === kind).flatMap((p) => [p.base, p.candidate].flatMap((v) => protocol.bench.cpus.map((cpu) => ({ name: `${p.name}/${v}`, cpu }))))
      const cases = report.benchmarks?.[kind]
      if (!Array.isArray(cases) || cases.length !== expected.length) throw new Error("missing comparison cases")
      for (const key of expected) {
        const found = cases.filter((c) => c.name === key.name && c.cpu === key.cpu)
        if (found.length !== 1 || found[0].samples?.length !== protocol.bench.count || !found[0].samples.every((s) => Number.isSafeInteger(s.iterations) && s.iterations > 0 && s.ns > 0) || !["ns", "bytes", "allocs"].every((metric) => Number.isFinite(found[0][metric]) && found[0][metric] >= 0 && found[0][metric] === median(found[0].samples.map((s) => s[metric])))) throw new Error("invalid comparison samples")
      }
    }
  }
  if (!candidate.accepted || !baseline.accepted || candidate.protocolHash !== baseline.protocolHash || JSON.stringify(candidate.environment.profile) !== JSON.stringify(baseline.environment.profile)) throw new Error("incompatible baseline/profile or incomplete run")
  for (const kind of ["core", "sql"]) {
    if (candidate.benchmarks[kind].length !== baseline.benchmarks[kind].length) throw new Error("missing comparison cases")
    for (const current of candidate.benchmarks[kind]) {
      const previous = baseline.benchmarks[kind].find((v) => v.name === current.name && v.cpu === current.cpu)
      if (!previous) throw new Error("missing comparison case")
      if (current.ns > previous.ns * (1 + protocol.regression.nsPercent / 100) || current.bytes > previous.bytes * (1 + protocol.regression.bytesPercent / 100) || current.allocs > previous.allocs + protocol.regression.allocDelta) throw new Error(`performance regression: ${current.name}@${current.cpu}`)
    }
  }
}

function finiteNumbers(value) {
  if (typeof value === "number" && !Number.isFinite(value)) throw new Error("non-finite report value")
  if (value && typeof value === "object") for (const child of Object.values(value)) finiteNumbers(child)
}

export function summarize(input) {
  const { manifest, runtime, soaks, goEnv } = input
  finiteNumbers(input)
  if (manifest.cleanupSucceeded !== true) throw new Error("infrastructure cleanup did not complete")
  if (!["full", "smoke"].includes(manifest.mode) || manifest.protocolHash !== protocolHash || runtime.protocolHash !== protocolHash || runtime.goos !== "linux" || runtime.goarch !== "amd64" || !runtime.cpuModel || !runtime.go || !runtime.services?.postgresql || !runtime.services?.mysql) throw new Error("invalid runtime/protocol metadata")
  if (!Number.isSafeInteger(runtime.cpus) || runtime.cpus < 2 || !runtime.kernel || !manifest.host?.cpuModel || !manifest.docker?.serverVersion || !Number.isSafeInteger(manifest.docker.memoryBytes) || manifest.docker.memoryBytes <= 0 || !/^[a-f0-9]{64}$/.test(manifest.serviceConfigHash) || !Number.isFinite(Date.parse(manifest.startedUTC)) || !(Date.parse(manifest.finishedUTC) >= Date.parse(manifest.startedUTC))) throw new Error("incomplete environment metadata")
  if (goEnv?.GOVERSION !== runtime.go || goEnv.GOOS !== runtime.goos || goEnv.GOARCH !== runtime.goarch || !["0", "1"].includes(goEnv.CGO_ENABLED) || goEnv.GOFLAGS !== "-p=2 -mod=readonly") throw new Error("Go build profile differs")
  const [quota, period] = runtime.cpuQuota.split(/\s+/).map(Number)
  if (!(period > 0) || quota / period !== protocol.worker.cpus || Number(runtime.memoryLimit) !== protocol.worker.memoryBytes || runtime.gomaxprocs !== 2) throw new Error("worker resource profile differs")
  if (manifest.mode === "full" && (manifest.source.cleanBefore !== true || manifest.source.cleanAfter !== true || !/^[a-f0-9]{40}$/.test(manifest.source.head))) throw new Error("acceptance requires a clean, committed source tree")
  const settings = manifest.mode === "full" ? protocol.bench : { count: 1, cpus: [2] }
  const benchmarks = { core: parseBenchmarks(input.core, "core", settings), sql: parseBenchmarks(input.sql, "sql", settings) }
  if (!Array.isArray(soaks) || soaks.length !== 4) throw new Error("missing soak reports")
  for (const dialect of ["postgresql", "mysql"]) for (const variant of ["NetHTTP", "Chitu"]) {
    const report = soaks.find((s) => s.dialect === dialect && s.variant === variant)
    if (!report?.accepted || report.protocolHash !== protocolHash || report.mode !== manifest.mode || report.phases?.length !== 5 || report.samples?.filter((s) => s.phase === "recovery").length < 6) throw new Error(`incomplete/failed soak: ${dialect}/${variant}`)
    if (!report.phases.every((p, i) => p.name === protocol.soak.phases[i].name && p.scheduled > 0)) throw new Error("invalid soak phases")
  }
  return {
    schema: 1, protocol: protocol.version, protocolHash, mode: manifest.mode, accepted: manifest.mode === "full", source: manifest.source,
    startedUTC: manifest.startedUTC, finishedUTC: manifest.finishedUTC,
    environment: { host: manifest.host, docker: manifest.docker, runtime, goEnv, profile: { go: runtime.go, goos: runtime.goos, goarch: runtime.goarch, cpuModel: runtime.cpuModel, cpus: runtime.cpus, gomaxprocs: runtime.gomaxprocs, kernel: runtime.kernel, cpuQuota: runtime.cpuQuota, memoryLimit: runtime.memoryLimit, services: runtime.services, serviceConfigHash: manifest.serviceConfigHash, dockerVersion: manifest.docker.serverVersion, hostMemoryBytes: manifest.host.memoryBytes, dockerMemoryBytes: manifest.docker.memoryBytes, goEnv } },
    benchmarks, soaks,
  }
}

export function markdown(report) {
  const lines = [`# Performance ${report.mode}: ${protocol.version}`, "", `Source: \`${report.source.head}\`. Run: ${report.startedUTC} → ${report.finishedUTC}.`, "", `CPU: ${report.environment.runtime.cpuModel}; Go: ${report.environment.runtime.go}; GOMAXPROCS 1/2; worker 2 CPU / 1 GiB.`, "", "Medians of five 500ms samples for a full run; MAD is median absolute deviation. Routing uses all routes round-robin. In-process timings exclude network; SQL timings include the actual driver/server.", "", "| Workload | CPU | Standard ns/op | Chitu ns/op | Ratio | Chitu B/op | allocs/op | Chitu MAD |", "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"]
  for (const pair of pairs) for (const cpu of report.mode === "full" ? protocol.bench.cpus : [2]) {
    const values = report.benchmarks[pair.kind]
    const base = values.find((v) => v.name === `${pair.name}/${pair.base}` && v.cpu === cpu)
    const candidate = values.find((v) => v.name === `${pair.name}/${pair.candidate}` && v.cpu === cpu)
    lines.push(`| ${pair.name.replace("Benchmark", "")} | ${cpu} | ${base.ns.toFixed(1)} | ${candidate.ns.toFixed(1)} | ${(candidate.ns / base.ns).toFixed(2)}× | ${candidate.bytes} | ${candidate.allocs} | ${candidate.madPercent.toFixed(1)}% |`)
  }
  lines.push("", "| Soak | Healthy success | Recovery p99 ms | Fault 503 min | Heap after-before MiB | Recovery slope B/min | Goroutines before→after | Server connections before→after |", "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
  for (const soak of report.soaks) {
    const healthy = soak.phases.filter((p) => ["healthy", "recovery"].includes(p.name))
    const success = healthy.reduce((sum, p) => sum + p.success, 0) / healthy.reduce((sum, p) => sum + p.scheduled, 0) * 100
    const fault = Math.min(...soak.phases.filter((p) => ["pool", "slow"].includes(p.name)).map((p) => p.unavailable / p.scheduled * 100))
    lines.push(`| ${soak.dialect}/${soak.variant} | ${success.toFixed(2)}% | ${soak.phases[4].p99MS.toFixed(1)} | ${fault.toFixed(2)}% | ${((soak.after.heapBytes - soak.before.heapBytes) / 1048576).toFixed(2)} | ${soak.recoverySlopeBytesPerMinute.toFixed(0)} | ${soak.before.goroutines}→${soak.after.goroutines} | ${soak.serverBefore}→${soak.serverAfter} |`)
  }
  lines.push("", "Server connection counts include the one observation connection, which is then closed. Histograms use 100µs upper-bound buckets. Soak memory includes client/server/generator and post-GC probes. This finite run establishes a baseline; operational RC observation remains separate.", "", "Raw inputs: [core](core.txt), [SQL](sql.txt), [runtime](runtime.json), [manifest](manifest.json), individual soak JSON files, and [combined report](report.json).", "")
  return lines.join("\n")
}

export function buildReport(directory, baseline) {
  const readJSON = (name) => JSON.parse(readFileSync(join(directory, name), "utf8"))
  const report = summarize({ manifest: readJSON("manifest.json"), runtime: readJSON("runtime.json"), goEnv: readJSON("go-env.json"), core: readFileSync(join(directory, "core.txt"), "utf8"), sql: readFileSync(join(directory, "sql.txt"), "utf8"), soaks: ["postgresql", "mysql"].flatMap((d) => ["NetHTTP", "Chitu"].map((v) => readJSON(`soak-${d}-${v}.json`))) })
  if (baseline) compareReports(report, JSON.parse(readFileSync(baseline, "utf8")))
  writeFileSync(join(directory, "report.json"), JSON.stringify(report, null, 2) + "\n")
  writeFileSync(join(directory, "report.md"), markdown(report))
  return report
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    if (process.argv.length < 3 || process.argv.length > 4) throw new Error("usage: node scripts/performance-report.mjs result-directory [baseline-report.json]")
    const report = buildReport(resolve(process.argv[2]), process.argv[3])
    console.log(`performance: ${report.mode} report complete (${report.benchmarks.core.length + report.benchmarks.sql.length} cases, ${report.soaks.length} soaks)`)
  } catch (error) { console.error(`performance: ${error.message}`); process.exitCode = 1 }
}
