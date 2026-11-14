import { spawnSync } from "node:child_process"
import { mkdirSync, readFileSync, writeFileSync } from "node:fs"
import { createHash, randomUUID } from "node:crypto"
import { cpus, totalmem, release, platform, arch } from "node:os"
import { resolve, join, dirname } from "node:path"
import { fileURLToPath } from "node:url"
import { protocol, protocolHash, buildReport } from "./performance-report.mjs"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const mode = process.argv[2] ?? "full"
if (!["full", "smoke"].includes(mode)) throw new Error("usage: node scripts/performance.mjs [full|smoke]")
const id = randomUUID().slice(0, 12)
const project = `chitu-perf-${id}`
const directory = join(root, ".cache", "performance", `run-${Date.now()}-${id}`)
mkdirSync(directory, { recursive: true })
const capture = (cmd, args, env = process.env) => {
  const child = spawnSync(cmd, args, { cwd: root, env, encoding: "utf8", maxBuffer: 32 * 1024 * 1024, timeout: 60_000 })
  if (child.error || child.status !== 0) throw new Error(`${cmd} ${args[0]} failed: ${child.error?.message ?? child.stderr}`)
  return child.stdout.trim()
}
const head = capture("git", ["rev-parse", "HEAD"])
const cleanBefore = !capture("git", ["status", "--porcelain"])
const serviceConfigHash = createHash("sha256").update(["compose.integration.yml", "compose.performance.yml", "integration/Dockerfile.performance"].map((file) => readFileSync(join(root, file), "utf8").replaceAll("\r\n", "\n")).join("\n")).digest("hex")
const info = JSON.parse(capture("docker", ["info", "--format", "{{json .}}"]))
const manifest = { schema: 1, mode, protocolHash, source: { head, cleanBefore }, serviceConfigHash, startedUTC: new Date().toISOString(), host: { cpuModel: cpus()[0].model, logicalCPUs: cpus().length, memoryBytes: totalmem(), os: platform(), arch: arch(), release: release() }, docker: { serverVersion: info.ServerVersion, kernel: info.KernelVersion, cpus: info.NCPU, memoryBytes: info.MemTotal }, project }
const save = () => writeFileSync(join(directory, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n")
save()
const env = { ...process.env, PERF_RESULT_DIR: directory, PERF_SMOKE: mode === "smoke" ? "1" : "0", PERF_BENCH_TIME: mode === "smoke" ? "1x" : protocol.bench.time, PERF_BENCH_COUNT: mode === "smoke" ? "1" : String(protocol.bench.count), PERF_BENCH_CPUS: mode === "smoke" ? "2" : protocol.bench.cpus.join(",") }
const compose = ["compose", "-p", project, "-f", "compose.integration.yml", "-f", "compose.performance.yml"]
const run = (args) => {
  const result = spawnSync("docker", [...compose, ...args], { cwd: root, env, stdio: "inherit", timeout: 25 * 60_000 })
  if (result.error || result.status !== 0) throw new Error(`docker ${args[0]} failed: ${result.error?.message ?? result.status}`)
}
let failed = false
try {
  if (mode === "full" && !cleanBefore) throw new Error("full acceptance requires committed source; use smoke while editing")
  console.log(`performance: ${mode}, artifacts ${directory}`)
  run(["build", "performance"])
  run(["up", "--abort-on-container-exit", "--exit-code-from", "performance", "performance"])
  console.log(`performance: ${mode} measurements finished; cleaning up`)
} catch (error) {
  failed = true
  manifest.error = error.message
  manifest.finishedUTC = new Date().toISOString()
  save()
  console.error(`performance: ${error.message}; artifacts ${directory}`)
} finally {
  let cleanupOK = true
  try { writeFileSync(join(directory, "compose.log"), capture("docker", [...compose, "logs", "--no-color"], env)) } catch (error) { console.error(error.message); failed = true; cleanupOK = false; manifest.error = error.message }
  try { run(["down", "--volumes", "--remove-orphans", "--timeout", "10"]) } catch (error) { console.error(error.message); failed = true; cleanupOK = false; manifest.error = error.message }
  manifest.cleanupSucceeded = cleanupOK
  manifest.finishedUTC = new Date().toISOString()
  save()
}
if (!failed) {
  try {
    manifest.source.cleanAfter = !capture("git", ["status", "--porcelain"]) && head === capture("git", ["rev-parse", "HEAD"])
    manifest.finishedUTC = new Date().toISOString()
    save()
    const report = buildReport(directory)
    writeFileSync(join(root, ".cache", "performance", mode === "full" ? "latest.json" : "latest-smoke.json"), JSON.stringify({ directory, accepted: report.accepted }) + "\n")
    console.log(`performance: ${mode} passed; report ${join(directory, "report.md")}`)
  } catch (error) {
    failed = true
    manifest.error = error.message
    save()
    console.error(`performance: ${error.message}; artifacts ${directory}`)
  }
}
if (failed) process.exitCode = 1
