import assert from "node:assert/strict"
import test from "node:test"
import { mkdtempSync, writeFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { gzipSync } from "node:zlib"
import { phases, protocolHash, scenarioNames, summarize, markdown } from "../release/operations/report.mjs"
import { sha256 } from "../release/operations/report.mjs"
import { verify } from "../release/operations/verify.mjs"

function fixture(mode = "full") {
  const manifest = { mode, protocolHash, observationSeconds: 600, source: { head: "a".repeat(40), cleanBefore: true, cleanAfter: true }, candidate: { version: "v0.1.0-rc.1", revision: "b".repeat(40), sourceSHA256: "c".repeat(64) }, appSourceSHA256: "d".repeat(64), cleanupSucceeded: true }
  const rate = mode === "full" ? 20 : 5
  const requests = phases.flatMap(([phase, seconds]) => Array.from({ length: (mode === "full" ? seconds : 8)*rate }, (_, i) => ({ id: `${phase}-${i}`, phase, status: 202, latencyMS: 5, lagMS: 1, replica: i%2 ? "a" : "b", scheme: "https", host: "rc.test" })))
  const resources = Array.from({ length: 120 }, (_, i) => ({ seconds: i*4, docker: ["sql", "cache", "broker", "proxy"].map((name) => ({ name: "reference-"+name, cpu: "2.50%", memory: "10MiB / 512MiB" })), apps: [{ phase: i < 12 ? "baseline" : "final", replica: "a", heapBytes: 100000, rssBytes: 1000000, goroutines: 20, sqlOpen: 4, sqlInUse: 2, jobFailures: 0 }] }))
  const events = scenarioNames.map((name) => ({ name, passed: true, durationMS: 2000, recoveryMS: 1000 }))
  const event = (name, values) => Object.assign(events.find((e) => e.name === name), values)
  event("rolling", { additiveMigration: true, oldRevisionRollback: true, drainMS: 1000 })
  event("rotation", { overlapBothAccepted: true, retiredKeyRejected: true, caVerified: true, tlsBefore: "old", tlsAfter: "new" })
  event("backup-restore", { rpoOperations: 0, rtoMS: 1000, newDatabase: true, restoredTLSApp: true, rowsSHA256: "a".repeat(64), dumpSHA256: "b".repeat(64) })
  event("migration-recovery", { dirtyBlocked: true, inspectedSchema: true, exactChecksumResolve: true, repeatedUpNoop: true })
  event("reconcile", { accepted: requests.length, persisted: requests.length, effectsExactlyOne: true, duplicateIDChecked: true })
  event("shutdown", { gracefulExitZero: true, noOOM: true })
  return { manifest, requests, resources, events }
}
const report = (f) => summarize(f.manifest, f.requests, f.resources, f.events)
test("operational full acceptance requires complete load and evidence; smoke cannot accept RC", () => {
  const full = report(fixture())
  assert.equal(full.accepted, true)
  assert.equal(full.total.requests, 12000)
  assert.match(markdown(full), /faults included/)
  assert.equal(report(fixture("smoke")).accepted, false)
})
test("operational gates reject missing evidence, false availability, latency, leaks and failed recovery", () => {
  for (const mutate of [
    (f) => { f.manifest.source.cleanBefore = false },
    (f) => { f.manifest.source.cleanAfter = false },
    (f) => { f.manifest.cleanupSucceeded = false },
    (f) => { f.manifest.protocolHash = "wrong" },
    (f) => { f.manifest.error = "container failed" },
    (f) => { f.manifest.observationSeconds = 599 },
    (f) => { f.events.pop() },
    (f) => { f.events[0].passed = false },
    (f) => { f.events.find((e) => e.name === "sql-outage").recoveryMS = 30001 },
    (f) => { f.events.find((e) => e.name === "backup-restore").rpoOperations = 1 },
    (f) => { f.events.find((e) => e.name === "reconcile").accepted-- },
    (f) => { f.requests.pop() },
    (f) => { f.requests[1].id = f.requests[0].id },
    (f) => { f.requests[0].latencyMS = NaN },
    (f) => { f.requests[0].latencyMS = 2101 },
    (f) => { f.requests[0].lagMS = 1001 },
    (f) => { f.requests.forEach((r) => { if (r.phase === "baseline") r.latencyMS = 251 }) },
    (f) => { f.requests[0].status = 503; f.requests[1].status = 503 },
    (f) => { f.requests[0].scheme = "http" },
    (f) => { f.resources.length = 1 },
    (f) => { delete f.resources[0].docker },
    (f) => { f.resources[2].seconds = 20 },
    (f) => { f.resources[0].apps[0].rssBytes = 257*1024**2 },
    (f) => { f.resources[0].apps[0].sqlOpen = 9 },
    (f) => { f.resources[0].apps[0].jobFailures = 1 },
    (f) => { f.resources.forEach((r) => { if (r.apps[0].phase === "final") r.apps[0].goroutines = 37 }) },
  ]) {
    const input = fixture(); mutate(input); assert.throws(() => report(input), undefined, mutate.toString())
  }
})
test("fault errors remain in overall error rate", () => {
  const input = fixture()
  input.requests.find((r) => r.phase === "sql-outage").status = 503
  input.events.find((e) => e.name === "reconcile").accepted--
  const result = report(input)
  assert.equal(result.total.success, 11999)
  assert.equal(result.phases.find((p) => p.name === "sql-outage").errors, 1)
  assert.ok(result.total.errorRate > 0)
})
test("independent artifact verification checks compressed raw data, tampering and backup identity", () => {
  const directory = mkdtempSync(join(tmpdir(), "chitu-ops-evidence-"))
  try {
    const input = fixture()
    const dump = Buffer.from("synthetic backup fixture")
    input.events.find((e) => e.name === "backup-restore").dumpSHA256 = sha256(dump)
    const files = { "manifest.json": JSON.stringify(input.manifest), "requests.ndjson": input.requests.map((r) => JSON.stringify(r)).join("\n")+"\n", "resources.json": JSON.stringify(input.resources), "events.json": JSON.stringify(input.events), "report.json": JSON.stringify(report(input)), "backup.dump": dump }
    const sums = {}
    for (const [name, data] of Object.entries(files)) {
      sums[name] = sha256(data)
      writeFileSync(join(directory, name+".gz"), gzipSync(data))
    }
    writeFileSync(join(directory, "SHA256SUMS.json"), JSON.stringify(sums))
    assert.equal(verify(directory).accepted, true)
    writeFileSync(join(directory, "requests.ndjson.gz"), gzipSync("tampered\n"))
    assert.throws(() => verify(directory), /artifact checksum/)
    delete sums["backup.dump"]
    writeFileSync(join(directory, "SHA256SUMS.json"), JSON.stringify(sums))
    assert.throws(() => verify(directory), /artifact checksums/)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
