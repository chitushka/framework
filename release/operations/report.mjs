import assert from "node:assert/strict"
import { createHash } from "node:crypto"
import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

export const phases = [
  ["baseline", 60], ["rolling", 60], ["rotation", 60], ["sql-outage", 60],
  ["cache-outage", 60], ["queue-outage", 90], ["app-crash", 60], ["final", 150],
]
export const protocolHash = createHash("sha256").update(readFileSync(fileURLToPath(new URL("../../docs/operations/protocol-v1.md", import.meta.url)), "utf8").replaceAll("\r\n", "\n")).digest("hex")
export const sha256 = (value) => createHash("sha256").update(value).digest("hex")
export function percentile(values, p) {
  assert.ok(values.length > 0 && values.every((v) => Number.isFinite(v) && v >= 0), "missing/invalid observations")
  const sorted = [...values].sort((a, b) => a - b)
  return sorted[Math.max(0, Math.ceil(sorted.length * p) - 1)]
}
function memoryBytes(value) {
  const match = value?.match(/^([0-9.]+)(B|KiB|MiB|GiB)$/)
  assert.ok(match, "invalid container memory sample")
  const bytes = Number(match[1]) * ({ B: 1, KiB: 1024, MiB: 1024**2, GiB: 1024**3 }[match[2]])
  assert.ok(Number.isFinite(bytes), "invalid container memory value")
  return bytes
}
export const scenarioNames = ["rolling", "rotation", "sql-outage", "cache-outage", "queue-outage", "app-crash", "reconcile", "backup-restore", "migration-recovery", "shutdown"]

export function summarize(manifest, requests, resources, events) {
  assert.equal(manifest.protocolHash, protocolHash, "protocol mismatch")
  assert.ok(["full", "smoke"].includes(manifest.mode), "invalid mode")
  assert.match(manifest.source.head, /^[a-f0-9]{40}$/)
  assert.match(manifest.candidate.sourceSHA256, /^[a-f0-9]{64}$/)
  assert.match(manifest.appSourceSHA256, /^[a-f0-9]{64}$/)
  assert.ok(manifest.cleanupSucceeded, "cleanup failed")
  assert.ok(!manifest.error, manifest.error)
  if (manifest.mode === "full") assert.ok(manifest.source.cleanBefore && manifest.source.cleanAfter, "acceptance requires unchanged committed source")
  if (manifest.mode === "full") assert.ok(manifest.observationSeconds >= 600 && manifest.observationSeconds <= 606, "observation duration")
  for (const name of scenarioNames) {
    const found = events.filter((event) => event.name === name)
    assert.equal(found.length, 1, `missing/duplicate scenario: ${name}`)
    assert.equal(found[0].passed, true, `failed scenario: ${name}`)
    assert.ok(Number.isFinite(found[0].durationMS) && found[0].durationMS >= 0, `invalid scenario duration: ${name}`)
  }
  const event = (name) => events.find((e) => e.name === name)
  for (const name of ["sql-outage", "cache-outage", "queue-outage", "app-crash", "migration-recovery"]) assert.ok(Number.isFinite(event(name).recoveryMS) && event(name).recoveryMS >= 0 && event(name).recoveryMS <= (name === "app-crash" ? 10_000 : name === "queue-outage" || name === "migration-recovery" ? 60_000 : 30_000), `recovery SLO: ${name}`)
  assert.ok(event("rolling").additiveMigration && event("rolling").oldRevisionRollback && event("rolling").drainMS < 15_000, "rolling evidence")
  assert.ok(event("rotation").overlapBothAccepted && event("rotation").retiredKeyRejected && event("rotation").caVerified && event("rotation").tlsBefore !== event("rotation").tlsAfter, "rotation evidence")
  assert.ok(event("backup-restore").rpoOperations === 0 && event("backup-restore").rtoMS <= 60_000 && event("backup-restore").newDatabase && event("backup-restore").restoredTLSApp, "backup evidence")
  assert.match(event("backup-restore").rowsSHA256, /^[a-f0-9]{64}$/)
  assert.match(event("backup-restore").dumpSHA256, /^[a-f0-9]{64}$/)
  assert.ok(event("migration-recovery").dirtyBlocked && event("migration-recovery").inspectedSchema && event("migration-recovery").exactChecksumResolve && event("migration-recovery").repeatedUpNoop, "migration evidence")
  assert.ok(event("shutdown").gracefulExitZero && event("shutdown").noOOM, "shutdown evidence")
  const rate = manifest.mode === "full" ? 20 : 5
  const seen = new Set()
  const summary = phases.map(([name, seconds]) => {
    const duration = manifest.mode === "full" ? seconds : 8
    const rows = requests.filter((r) => r.phase === name)
    assert.equal(rows.length, duration * rate, `missing samples: ${name}`)
    for (const row of rows) {
      assert.ok(typeof row.id === "string" && !seen.has(row.id), "duplicate/missing request ID")
      seen.add(row.id)
      assert.ok(Number.isFinite(row.status) && row.status >= 0 && row.status <= 599, "invalid status")
      assert.ok(row.latencyMS <= 2100, "request deadline exceeded")
      if (row.status === 202) assert.ok(row.scheme === "https" && row.host === "rc.test" && ["a", "b"].includes(row.replica), "bad TLS proxy response")
    }
    const success = rows.filter((r) => r.status === 202).length
    const p99MS = percentile(rows.map((r) => r.latencyMS), .99)
    const lagP99MS = percentile(rows.map((r) => r.lagMS), .99)
    const maxLagMS = Math.max(...rows.map((r) => r.lagMS))
    assert.ok(lagP99MS <= 100 && maxLagMS <= 1000, `load generator lag: ${name}`)
    if (["baseline", "rolling", "rotation", "final"].includes(name)) {
      assert.ok(success / rows.length >= .999, `availability SLO: ${name}`)
      assert.ok(p99MS <= 250, `latency SLO: ${name}`)
    }
    return { name, seconds: duration, requests: rows.length, success, errors: rows.length - success, errorRate: 1 - success / rows.length, p99MS, lagP99MS, maxLagMS }
  })
  assert.equal(requests.length, summary.reduce((n, p) => n + p.requests, 0), "unexpected phase samples")
  assert.equal(event("reconcile").accepted, requests.filter((r) => r.status === 202).length, "reconciliation count")
  assert.ok(event("reconcile").effectsExactlyOne && event("reconcile").duplicateIDChecked && event("reconcile").persisted >= event("reconcile").accepted, "reconciliation evidence")
  if (manifest.mode === "full") for (const replica of ["a", "b"]) assert.ok(requests.some((r) => r.status === 202 && r.phase === "rolling" && r.replica === replica), "both rolling replicas must serve real load")
  // /status samples must cover both sides of the observation, including the
  // stable end. Faults may make a sample unavailable, but never erase its slot.
  assert.ok(resources.length >= (manifest.mode === "full" ? 100 : 8), "missing resource observations")
  assert.ok(resources.every((r) => Number.isFinite(r.seconds) && r.seconds >= 0 && Array.isArray(r.docker) && r.docker.length >= 4 && !r.error), "missing container CPU/memory samples")
  for (let i = 1; i < resources.length; i++) assert.ok(resources[i].seconds-resources[i-1].seconds <= 5, "resource observation gap")
  const containers = {}
  for (const sample of resources.flatMap((r) => r.docker)) {
    assert.match(sample.name, /^reference-(sql|cache|broker|proxy|a|b)$/)
    assert.match(sample.cpu, /^[0-9.]+%$/)
    const cpuPercent = Number(sample.cpu.slice(0, -1)), bytes = memoryBytes(sample.memory.split(" / ")[0])
    assert.ok(Number.isFinite(cpuPercent), "invalid CPU sample")
    const previous = containers[sample.name] ?? { maxCPUPercent: 0, maxMemoryBytes: 0 }
    containers[sample.name] = { maxCPUPercent: Math.max(previous.maxCPUPercent, cpuPercent), maxMemoryBytes: Math.max(previous.maxMemoryBytes, bytes) }
  }
  const valid = resources.flatMap((r) => r.apps ?? [])
  assert.ok(valid.some((r) => r.phase === "baseline") && valid.some((r) => r.phase === "final"), "missing baseline/final resources")
  for (const row of valid) {
    for (const key of ["heapBytes", "rssBytes", "goroutines", "sqlOpen", "sqlInUse", "jobFailures"]) assert.ok(Number.isFinite(row[key]) && row[key] >= 0, `missing resource counter: ${key}`)
    assert.ok(row.rssBytes > 0 && row.rssBytes <= 256 * 1024 ** 2 && row.heapBytes <= 128 * 1024 ** 2 && row.goroutines <= 128 && row.sqlOpen <= 8 && row.sqlInUse <= 8 && row.jobFailures === 0, "resource SLO")
  }
  const baseline = valid.filter((r) => r.phase === "baseline" && r.replica === "a")
  const final = valid.filter((r) => r.phase === "final" && r.replica === "a")
  assert.ok(baseline.length && final.length, "replica a resource continuity")
  assert.ok(percentile(final.map((r) => r.goroutines), .5) <= percentile(baseline.map((r) => r.goroutines), .5) + 16, "goroutine growth")
  return { accepted: manifest.mode === "full", mode: manifest.mode, candidate: manifest.candidate, protocolHash, source: manifest.source, phases: summary, total: { requests: requests.length, success: requests.filter((r) => r.status === 202).length, errorRate: requests.filter((r) => r.status !== 202).length / requests.length, p99MS: percentile(requests.map((r) => r.latencyMS), .99) }, resources: { samples: resources.length, maxRSSBytes: Math.max(...valid.map((r) => r.rssBytes)), maxHeapBytes: Math.max(...valid.map((r) => r.heapBytes)), maxGoroutines: Math.max(...valid.map((r) => r.goroutines)), maxSQLConnections: Math.max(...valid.map((r) => r.sqlOpen)), containers }, events }
}

export function markdown(report) {
  return `# RC operational ${report.mode}\n\nAccepted: **${report.accepted}**. Candidate ${report.candidate.version}, source \`${report.candidate.revision}\`.\n\nHarness commit \`${report.source.head}\`; protocol SHA-256 \`${report.protocolHash}\`.\n\n| Phase | Seconds | Requests | Errors | Error rate | p99 ms | Lag p99 ms |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: |\n${report.phases.map((p) => `| ${p.name} | ${p.seconds} | ${p.requests} | ${p.errors} | ${(100*p.errorRate).toFixed(3)}% | ${p.p99MS.toFixed(2)} | ${p.lagP99MS.toFixed(2)} |`).join("\n")}\n\nTotal: ${report.total.requests} requests, error rate ${(100*report.total.errorRate).toFixed(3)}%, p99 ${report.total.p99MS.toFixed(2)}ms (faults included).\n\nResources: ${report.resources.samples} samples, max RSS ${(report.resources.maxRSSBytes/1024**2).toFixed(2)}MiB, heap ${(report.resources.maxHeapBytes/1024**2).toFixed(2)}MiB, goroutines ${report.resources.maxGoroutines}, SQL pool ${report.resources.maxSQLConnections}.\n\n${report.events.map((e) => `- ${e.name}: ${JSON.stringify(e)}`).join("\n")}\n`
}
