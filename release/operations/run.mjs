import assert from "node:assert/strict"
import { execFile } from "node:child_process"
import { promisify } from "node:util"
import { randomBytes, generateKeyPairSync, sign, X509Certificate } from "node:crypto"
import { readFileSync, writeFileSync, mkdirSync, cpSync, renameSync, existsSync } from "node:fs"
import { resolve, join, dirname } from "node:path"
import { fileURLToPath } from "node:url"
import { cpus, totalmem, platform, arch } from "node:os"
import { request as httpsRequest, Agent } from "node:https"
import { phases, protocolHash, sha256, summarize, markdown } from "./report.mjs"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..")
const mode = process.argv[2] ?? "full"
assert.ok(["full", "smoke"].includes(mode), "usage: node release/operations/run.mjs [full|smoke] [candidate-directory]")
const frozen = JSON.parse(readFileSync(join(root, "docs/releases/v0.1.0-rc.1/release.json")))
const latest = join(root, ".cache/releases/latest.txt")
let previousAccepted
if (existsSync(latest)) {
  const directory = readFileSync(latest, "utf8").trim()
  const metadata = JSON.parse(readFileSync(join(directory, "release.json")))
  if (metadata.sourceSHA256 === frozen.sourceSHA256) previousAccepted = directory
}
// A later stable draft must not silently become the operational acceptance
// object. Reconstruct the exact frozen RC when latest points at another release.
const suppliedDirectory = process.argv[3] ?? previousAccepted
const candidate = suppliedDirectory ? JSON.parse(readFileSync(join(resolve(suppliedDirectory), "release.json"))) : frozen
assert.equal(candidate.sourceSHA256, frozen.sourceSHA256, "candidate differs from frozen acceptance object")
assert.equal(candidate.revision, frozen.revision)
assert.equal(candidate.version, frozen.version)
const id = `chitu-ops-${randomBytes(6).toString("hex")}`
const out = join(root, ".cache/operations", id)
const context = join(out, "build"), config = join(out, "config")
mkdirSync(join(context, "candidate"), { recursive: true }); mkdirSync(config)
const sourceArchive = suppliedDirectory ? join(resolve(suppliedDirectory), "source.tar") : join(out, "source.tar")
const execute = promisify(execFile)
async function command(cmd, args, options = {}) {
  try { return (await execute(cmd, args, { cwd: root, windowsHide: true, maxBuffer: 32*1024**2, timeout: 120_000, ...options })).stdout }
  catch (error) { throw Error(`${cmd} ${args[0]} failed (exit ${error.code}): ${(error.stderr ?? "").toString().slice(-3000)}`) }
}
const docker = (...args) => command("docker", args)
const git = (...args) => command("git", args)
const delay = (ms) => new Promise((done) => setTimeout(done, ms))
const normalized = (file) => readFileSync(join(root, file), "utf8").replaceAll("\r\n", "\n")
const appFiles = ["integration/operations/main.go", "release/operations/Dockerfile", "release/operations/run.mjs", "release/operations/report.mjs", "release/operations/verify.mjs"]
const head = (await git("rev-parse", "HEAD")).trim()
const manifest = { schema: 1, mode, protocolHash, candidate: { version: candidate.version, revision: candidate.revision, sourceSHA256: candidate.sourceSHA256 }, appSourceSHA256: sha256(appFiles.map(normalized).join("\n")), source: { head, cleanBefore: !(await git("status", "--porcelain")).trim() }, startedUTC: new Date().toISOString(), host: { cpu: cpus()[0].model, logicalCPUs: cpus().length, memoryBytes: totalmem(), platform: platform(), arch: arch() }, images: {} }
const save = (name, value) => writeFileSync(join(out, name), JSON.stringify(value, null, 2)+"\n")
const requests = [], resources = [], events = [], containers = [], volumes = []
const activeApps = new Map()
let networkCreated = false, proxyIP, port, agent, currentToken, phase = "setup", resourceLoop, sampling = false
const servicePassword = randomBytes(24).toString("hex")
const names = Object.fromEntries(["sql", "cache", "broker", "proxy", "a", "b"].map((name) => [name, `${id}-${name}`]))
const images = {
  sql: "postgres:17-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b",
  cache: "redis:7.4-alpine@sha256:8da19c7399dca900c6822a1cf30f9d2285585e2c4b457ff4095b0f4500054d3f",
  broker: "rabbitmq:4.3-alpine@sha256:b1f997f74103d08fc1f20a13890dc44fe6cfa51d528cf521b480ada988d3ffa5",
  proxy: "nginx:alpine@sha256:713fc2ebfd92e8217db451477182ee91944b123d2b1ee9fd5a8c6e91ca6c3a11",
}
const imageTag = `${id}:reference`
let imageID
const keys = Object.fromEntries(["old", "new"].map((kid) => [kid, generateKeyPairSync("ed25519")]))
function keyRing(kids) {
  const data = kids.map((kid) => ({ id: kid, key: keys[kid].publicKey.export({ format: "der", type: "spki" }).subarray(-32).toString("base64"), ...(kid === "old" && kids.length > 1 ? { until: new Date(Date.now()+60_000).toISOString() } : {}) }))
  writeFileSync(join(config, "keys.tmp"), JSON.stringify(data)); renameSync(join(config, "keys.tmp"), join(config, "keys.json"))
}
function token(kid) {
  const b64 = (v) => Buffer.from(JSON.stringify(v)).toString("base64url")
  const now = Math.floor(Date.now()/1000)
  const body = `${b64({ alg: "EdDSA", typ: "at+jwt", kid })}.${b64({ sub: "operator", iss: "ops-issuer", aud: "ops-api", iat: now, exp: now+900 })}`
  return body+"."+sign(null, Buffer.from(body), keys[kid].privateKey).toString("base64url")
}
function nginx(apps, certificate = 1) {
  const upstream = apps.length ? `upstream reference { ${apps.map((replica) => `server ${names[replica]}:8080 max_fails=1 fail_timeout=1s;`).join(" ")} }` : ""
  const headers = "proxy_set_header Host rc.test; proxy_set_header X-Forwarded-Host rc.test; proxy_set_header X-Forwarded-Proto https; proxy_set_header X-Forwarded-For $remote_addr; proxy_connect_timeout 1s; proxy_read_timeout 2s; proxy_send_timeout 2s;"
  return `user nginx; worker_processes 1; error_log /dev/stderr warn; pid /tmp/nginx.pid; events { worker_connections 512; } http { access_log off; client_body_temp_path /tmp/body; proxy_temp_path /tmp/proxy; ${upstream} server { listen 8443 ssl; server_name rc.test; ssl_protocols TLSv1.2 TLSv1.3; ssl_certificate /run/config/tls${certificate}.crt; ssl_certificate_key /run/config/tls${certificate}.key; add_header Strict-Transport-Security "max-age=3600" always; ${[...activeApps.keys()].map((r) => `location = /replica/${r}/status { ${headers} proxy_pass http://${names[r]}:8080/status; }`).join(" ")} location / { ${apps.length ? `${headers} proxy_pass http://reference;` : "return 503;"} } } }\n`
}
let certificate = 1
async function route(apps) {
  writeFileSync(join(config, "nginx.conf"), nginx(apps, certificate))
  await docker("exec", names.proxy, "nginx", "-t", "-c", "/run/config/nginx.conf")
  await docker("exec", names.proxy, "nginx", "-s", "reload", "-c", "/run/config/nginx.conf")
  await delay(500)
}
async function request(path, { method = "GET", body, bearer = currentToken, headers = {}, fresh = false } = {}) {
  const start = performance.now()
  return new Promise((resolve, reject) => {
    const req = httpsRequest({ hostname: "127.0.0.1", port, servername: "rc.test", path, method, ca: readFileSync(join(config, "ca.crt")), agent: fresh ? false : agent, headers: { host: "rc.test", authorization: `Bearer ${bearer}`, "content-type": "application/json", ...headers } }, (res) => {
      let data = ""
      const fingerprint = res.socket.getPeerCertificate().fingerprint256
      res.on("data", (chunk) => { data += chunk; if (data.length > 1024*1024) res.destroy(Error("response exceeds limit")) })
      res.on("error", reject)
      res.on("end", () => { clearTimeout(timeout); let json; try { json = JSON.parse(data) } catch { json = {} }; resolve({ status: res.statusCode, latencyMS: performance.now()-start, fingerprint, ...json }) })
    })
    const timeout = setTimeout(() => req.destroy(Error("request deadline")), 2000)
    req.on("error", (err) => { clearTimeout(timeout); reject(err) })
    req.end(body === undefined ? undefined : JSON.stringify(body))
  })
}
async function waitFor(fn, milliseconds, description) {
  const start = performance.now(); let last
  while (performance.now()-start < milliseconds) {
    try { if (await fn()) return performance.now()-start } catch (error) { last = error }
    await delay(100)
  }
  throw Error(`${description} exceeded ${milliseconds}ms${last ? ": "+last.message : ""}`)
}
async function sql(statement, database = "chitu") {
  return (await docker("exec", names.sql, "psql", "-U", "chitu", "-d", database, "-At", "-v", "ON_ERROR_STOP=1", "-c", statement)).trim()
}
function appOptions(replica, revision = "1", database = "chitu") {
  return ["--network", id, "--read-only", "--user", "65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--cpus", "1", "--memory", "256m", "--tmpfs", "/data:rw,uid=65532,gid=65532,size=16m", "--mount", `type=bind,source=${config},target=/run/config,readonly`, "-e", `DB_DSN=postgres://chitu:${servicePassword}@sql:5432/${database}?sslmode=disable&connect_timeout=1&application_name=ops-${replica}`, "-e", `SERVICE_PASSWORD=${servicePassword}`, "-e", `AMQP_URL=amqp://chitu:${servicePassword}@broker:5672/`, "-e", `TRUSTED_PROXY=${proxyIP}/32`, "-e", `REPLICA=${replica}`, "-e", `APP_REVISION=${revision}`, "-e", "GOMAXPROCS=2"]
}
async function startApp(replica, revision = "1", database = "chitu") {
  containers.push(names[replica])
  await docker("run", "-d", "--name", names[replica], ...appOptions(replica, revision, database), imageID)
  activeApps.set(replica, revision)
  const info = JSON.parse(await docker("inspect", names[replica]))[0]
  assert.equal(info.Image, imageID); assert.equal(info.Config.User, "65532:65532"); assert.equal(info.HostConfig.ReadonlyRootfs, true)
}
async function job(action, { detached = false, name = `${id}-job` } = {}) {
  if (detached) containers.push(name)
  return docker("run", ...(detached ? ["-d"] : ["--rm"]), "--name", name, ...appOptions("job"), imageID, action)
}
async function sample() {
  const observedPhase = phase
  const row = { seconds: (performance.now()-observationStart)/1000, phase: observedPhase, apps: [] }
  await Promise.all([...activeApps.keys()].map(async (replica) => {
    try {
      const result = await request(`/replica/${replica}/status`)
      if (result.status === 200) row.apps.push({ ...result, phase: observedPhase })
    } catch { /* the failed slot remains in resources */ }
  }))
  // Listing running containers lets Docker handle removal during rolling update;
  // requesting a name that disappears halfway through stats would lose the slot.
  // Discard every unrelated container before retaining any resource metadata.
  try { row.docker = (await docker("stats", "--no-stream", "--format", "{{json .}}")).trim().split("\n").filter(Boolean).map(JSON.parse).filter(({ Name }) => Object.values(names).includes(Name)).map(({ Name, CPUPerc, MemUsage, PIDs }) => ({ name: Name.replace(id, "reference"), cpu: CPUPerc, memory: MemUsage, pids: PIDs })) } catch (error) { row.error = error.message }
  resources.push(row)
}
let observationStart = 0
async function load(name, seconds) {
  const rate = mode === "full" ? 20 : 5, count = seconds*rate, start = performance.now(), pending = new Set()
  for (let n = 0; n < count; n++) {
    const scheduled = start+n*1000/rate
    await delay(Math.max(0, scheduled-performance.now()))
    const lagMS = Math.max(0, performance.now()-scheduled), operationID = `${name}-${n}`
    assert.ok(pending.size < 64, "load concurrency exceeded")
    const task = (async () => {
      let result
      const began = performance.now()
      try { result = await request("/work", { method: "POST", body: { id: operationID } }) }
      catch (error) { result = { status: 0, latencyMS: performance.now()-began, error: error.message } }
      requests.push({ ...result, id: operationID, phase: name, lagMS, seconds: (began-observationStart)/1000 })
    })()
    pending.add(task); task.finally(() => pending.delete(task))
  }
  await Promise.all(pending)
  await delay(Math.max(0, start+seconds*1000-performance.now()))
}
async function recoveryProbe() {
  let consecutive = 0
  return waitFor(async () => { const result = await request("/status"); consecutive = result.status === 200 ? consecutive+1 : 0; return consecutive >= 10 }, 30_000, "10 healthy recovery probes")
}
async function scenario(name, action) {
  const start = performance.now()
  const evidence = await action()
  const event = { name, passed: true, durationMS: performance.now()-start, ...evidence }
  events.push(event)
  console.log(`operations: ${name} passed (${(event.durationMS/1000).toFixed(1)}s)`)
}
const downtime = mode === "full" ? 15_000 : 2000
const actions = {
  rolling: async () => {
    await job("expand")
    await startApp("b", "2"); await route(["a", "b"])
    await waitFor(async () => (await request("/replica/b/status")).status === 200, 10_000, "expanded replica")
    // Keep both revisions in rotation, then remove old upstream before SIGTERM.
    await delay(mode === "full" ? 5000 : 500)
    await route(["b"])
    const start = performance.now(); await docker("stop", "-t", "15", names.a)
    assert.equal(JSON.parse(await docker("inspect", names.a))[0].State.ExitCode, 0)
    const drainMS = performance.now()-start
    assert.ok(drainMS < 15_000)
    await docker("rm", names.a); activeApps.delete("a")
    await startApp("a", "1"); await route(["a", "b"])
    return { additiveMigration: true, oldRevisionRollback: (await request("/replica/a/status")).revision === "1", drainMS }
  },
  rotation: async () => {
    keyRing(["old", "new"]); await delay(500)
    for (const kid of ["old", "new"]) assert.equal((await request("/status", { bearer: token(kid) })).status, 200)
    currentToken = token("new"); await delay(2200)
    keyRing(["new"]); await delay(500)
    assert.equal((await request("/status", { bearer: token("old") })).status, 401)
    assert.equal((await request("/status")).status, 200)
    const before = (await request("/status", { fresh: true })).fingerprint
    certificate = 2; await route(["a", "b"])
    const after = (await request("/status", { fresh: true })).fingerprint
    assert.notEqual(before, after)
    assert.equal(after, new X509Certificate(readFileSync(join(config, "tls2.crt"))).fingerprint256)
    return { overlapBothAccepted: true, retiredKeyRejected: true, tlsBefore: before, tlsAfter: after, caVerified: true }
  },
  "sql-outage": async () => {
    await docker("kill", "--signal", "KILL", names.sql); await delay(downtime)
    const restarted = performance.now(); await docker("start", names.sql)
    await recoveryProbe()
    const recoveryMS = performance.now()-restarted
    assert.ok(recoveryMS <= 30_000)
    return { downtimeMS: downtime, recoveryMS, sameVolume: true }
  },
  "cache-outage": async () => {
    await docker("kill", "--signal", "KILL", names.cache); await delay(downtime)
    await docker("start", names.cache)
    return { downtimeMS: downtime, recoveryMS: await recoveryProbe(), coldCache: true }
  },
  "queue-outage": async () => {
    await docker("kill", "--signal", "KILL", names.broker); await delay(downtime)
    const target = Number(await sql("SELECT COUNT(*) FROM operations"))
    const began = performance.now(); await docker("start", names.broker)
    await waitFor(async () => Number(await sql("SELECT COUNT(*) FROM operations WHERE effects=1")) >= target, 60_000, "queue outbox catch-up")
    return { downtimeMS: downtime, recoveryMS: performance.now()-began, confirmedBeforeRestart: target }
  },
  "app-crash": async () => {
    await docker("kill", "--signal", "KILL", names.a)
    const began = performance.now()
    await waitFor(async () => (await request("/status")).status === 200, 10_000, "surviving replica")
    const recoveryMS = performance.now()-began
    await docker("start", names.a)
    return { recoveryMS, restartMS: performance.now()-began, secondReplicaSurvived: true }
  },
}

try {
  if (mode === "full") assert.ok(manifest.source.cleanBefore, "full requires committed source; use smoke while editing")
  console.log(`operations: ${mode}, artifacts ${out}`)
  manifest.docker = JSON.parse(await docker("info", "--format", "{{json .}}"))
  manifest.docker = { version: manifest.docker.ServerVersion, kernel: manifest.docker.KernelVersion, cpus: manifest.docker.NCPU, memoryBytes: manifest.docker.MemTotal }
  // The accepted 13.1 archive was exported with Git's CRLF checkout conversion;
  // pin it explicitly so Linux and Windows reconstruct those exact frozen bytes.
  if (!suppliedDirectory) await git("-c", "core.autocrlf=true", "archive", "--format=tar", "-o", sourceArchive, frozen.revision)
  assert.equal(sha256(readFileSync(sourceArchive)), frozen.sourceSHA256, "source archive checksum")
  const tar = process.platform === "win32" ? join(process.env.SystemRoot ?? "C:\\Windows", "System32/tar.exe") : "tar"
  await command(tar, ["-xf", sourceArchive, "-C", join(context, "candidate")])
  cpSync(join(root, "integration/operations"), join(context, "app"), { recursive: true })
  cpSync(join(root, "release/operations/Dockerfile"), join(context, "Dockerfile"))
  await command("docker", ["build", "--platform", "linux/amd64", "-t", imageTag, context], { timeout: 600_000 })
  imageID = (await docker("image", "inspect", imageTag, "--format", "{{.Id}}")).trim()
  manifest.images.reference = imageID
  for (const [name, image] of Object.entries(images)) {
    // A clean CI machine need not have these digests cached.
    try { await docker("image", "inspect", image) } catch { await command("docker", ["pull", image], { timeout: 300_000 }) }
    const info = JSON.parse(await docker("image", "inspect", image))[0]
    manifest.images[name] = { configured: image, id: info.Id }
  }
  // Generate private CA/leaf only in the ignored run directory.
  await docker("run", "--rm", "--user", "0:0", "--network=none", "--mount", `type=bind,source=${config},target=/certs`, imageID, "certs", "/certs")
  keyRing(["old"]); currentToken = token("old")
  await docker("network", "create", id); networkCreated = true
  for (const name of ["sql", "broker"]) { const volume = `${id}-${name}-data`; await docker("volume", "create", volume); volumes.push(volume) }
  const startService = async (name, options, image) => {
    containers.push(names[name]); await docker("run", "-d", "--name", names[name], "--network", id, "--network-alias", name, ...options, image)
  }
  await startService("sql", ["--cpus", "1", "--memory", "512m", "--mount", `type=volume,source=${volumes[0]},target=/var/lib/postgresql/data`, "-e", "POSTGRES_USER=chitu", "-e", `POSTGRES_PASSWORD=${servicePassword}`, "-e", "POSTGRES_DB=chitu"], images.sql)
  // Redis is explicitly disposable cache, so cold-start must be harmless.
  containers.push(names.cache)
  await docker("run", "-d", "--name", names.cache, "--network", id, "--network-alias", "cache", "--cpus", ".5", "--memory", "128m", images.cache, "redis-server", "--requirepass", servicePassword, "--save", "", "--appendonly", "no", "--maxmemory", "64mb", "--maxmemory-policy", "allkeys-lru")
  await startService("broker", ["--hostname", "ops-broker", "--cpus", "1", "--memory", "512m", "--mount", `type=volume,source=${volumes[1]},target=/var/lib/rabbitmq`, "-e", "RABBITMQ_DEFAULT_USER=chitu", "-e", `RABBITMQ_DEFAULT_PASS=${servicePassword}`, "-e", "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS=+S 2:2"], images.broker)
  writeFileSync(join(config, "nginx.conf"), nginx([]))
  containers.push(names.proxy)
  await docker("run", "-d", "--name", names.proxy, "--network", id, "--cpus", ".5", "--memory", "128m", "--mount", `type=bind,source=${config},target=/run/config,readonly`, "-p", "127.0.0.1::8443", images.proxy, "nginx", "-g", "daemon off;", "-c", "/run/config/nginx.conf")
  proxyIP = JSON.parse(await docker("inspect", names.proxy))[0].NetworkSettings.Networks[id].IPAddress
  port = Number((await docker("port", names.proxy, "8443/tcp")).trim().split(":").at(-1))
  agent = new Agent({ keepAlive: true, maxSockets: 64, maxFreeSockets: 8, ca: readFileSync(join(config, "ca.crt")) })
  await waitFor(async () => (await sql("SELECT 1")) === "1", 60_000, "PostgreSQL readiness")
  await waitFor(async () => { await docker("exec", names.broker, "su-exec", "rabbitmq", "rabbitmq-diagnostics", "-q", "check_running"); return true }, 90_000, "RabbitMQ readiness")
  await job("migrate")
  await startApp("a"); await route(["a"])
  await waitFor(async () => (await request("/status")).status === 200, 30_000, "TLS application readiness")
  assert.equal((await request("/status", { bearer: "invalid" })).status, 401)
  const origin = await request("/status", { headers: { "x-forwarded-proto": "http", "x-forwarded-host": "evil.test", "x-forwarded-for": "1.2.3.4" } })
  assert.equal(origin.scheme, "https"); assert.equal(origin.host, "rc.test")
  observationStart = performance.now(); sampling = true
  resourceLoop = (async () => { while (sampling) { const began = performance.now(); await sample(); await delay(Math.max(0, 4000-(performance.now()-began))) } })()
  for (const [name, fullSeconds] of phases) {
    phase = name
    console.log(`operations: observing ${name}`)
    const began = performance.now()
    const results = await Promise.allSettled([load(name, mode === "full" ? fullSeconds : 8), actions[name] ? scenario(name, actions[name]) : Promise.resolve()])
    for (const result of results) if (result.status === "rejected") throw result.reason
    if (mode === "full") assert.ok(performance.now()-began < fullSeconds*1000+1000, `scenario exceeded observation phase: ${name}`)
  }
  manifest.observationSeconds = (performance.now()-observationStart)/1000
  sampling = false; await resourceLoop
  await scenario("reconcile", async () => {
    const accepted = requests.filter((r) => r.status === 202).map((r) => r.id)
    await waitFor(async () => Number(await sql("SELECT COUNT(*) FROM operations WHERE effects=0 OR NOT enqueued")) === 0, 60_000, "all durable operations processed")
    const rows = (await sql("SELECT id || ':' || effects FROM operations ORDER BY id")).split("\n")
    const stored = new Map(rows.map((row) => row.split(":")))
    for (const operationID of accepted) assert.equal(stored.get(operationID), "1", "accepted operation lost or duplicated")
    for (const [, effects] of stored) assert.equal(effects, "1")
    // Repeat an accepted ID; SQL effect must stay one despite duplicate HTTP.
    assert.equal((await request("/work", { method: "POST", body: { id: accepted[0] } })).status, 202)
    assert.equal(await sql(`SELECT effects FROM operations WHERE id='${accepted[0]}'`), "1")
    for (const replica of activeApps.keys()) assert.equal((await request(`/replica/${replica}/status`)).jobFailures, 0)
    return { accepted: accepted.length, persisted: stored.size, ambiguousCommitted: stored.size-accepted.length, effectsExactlyOne: true, duplicateIDChecked: true }
  })
  await scenario("backup-restore", async () => {
    await route([])
    for (const replica of activeApps.keys()) { await docker("stop", "-t", "15", names[replica]); activeApps.delete(replica) }
    const before = await sql("SELECT row_to_json(t) FROM (SELECT * FROM operations ORDER BY id) t")
    const dump = await command("docker", ["exec", names.sql, "pg_dump", "-U", "chitu", "-d", "chitu", "-Fc"], { encoding: "buffer" })
    writeFileSync(join(out, "backup.dump"), dump)
    const began = performance.now()
    await sql("CREATE DATABASE restored")
    // pg_restore consumes stdin; execFile's input is not a supported option.
    const { spawn } = await import("node:child_process")
    await new Promise((done, reject) => {
      const child = spawn("docker", ["exec", "-i", names.sql, "pg_restore", "-U", "chitu", "-d", "restored", "--exit-on-error"], { windowsHide: true, stdio: ["pipe", "ignore", "pipe"] })
      let stderr = ""; child.stderr.on("data", (c) => { stderr += c })
      child.on("error", reject); child.on("close", (code) => code === 0 ? done() : reject(Error(`pg_restore failed: ${stderr}`)))
      child.stdin.on("error", reject); child.stdin.end(dump)
    })
    const after = await sql("SELECT row_to_json(t) FROM (SELECT * FROM operations ORDER BY id) t", "restored")
    assert.equal(after, before)
    await docker("rm", names.a)
    await startApp("a", "1", "restored"); await route(["a"])
    await waitFor(async () => (await request("/status")).status === 200, 10_000, "restored application readiness")
    const rtoMS = performance.now()-began
    assert.ok(rtoMS <= 60_000)
    return { rtoMS, rpoOperations: 0, rowsSHA256: sha256(before), dumpSHA256: sha256(dump), bytes: dump.length, newDatabase: true, restoredTLSApp: true }
  })
  await scenario("migration-recovery", async () => {
    const name = `${id}-migration`
    await job("crash-migration", { detached: true, name })
    await waitFor(async () => (await sql("SELECT to_regclass('recovery_evidence') IS NOT NULL")) === "t", 10_000, "nontransactional DDL")
    await docker("kill", "--signal", "KILL", name)
    const began = performance.now()
    // A disconnected PostgreSQL backend may still be sleeping. The operator
    // terminates only this test job's session before reacquiring its lock.
    const terminatedSessions = await sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='ops-job' AND query LIKE '%pg_sleep(60)%'")
    const output = await job("recover")
    assert.match(output, /PASS dirty blocks Up/)
    const recoveryMS = performance.now()-began
    assert.ok(recoveryMS < 60_000)
    return { recoveryMS, dirtyBlocked: true, inspectedSchema: true, exactChecksumResolve: true, repeatedUpNoop: true, terminatedSleepingSession: terminatedSessions.includes("t") }
  })
  await scenario("shutdown", async () => {
    for (const replica of activeApps.keys()) { await docker("stop", "-t", "15", names[replica]); assert.equal(JSON.parse(await docker("inspect", names[replica]))[0].State.ExitCode, 0) }
    for (const name of new Set(containers)) {
      const info = JSON.parse(await docker("inspect", name))[0]
      assert.equal(info.State.OOMKilled, false)
    }
    return { gracefulExitZero: true, noOOM: true }
  })
} catch (error) {
  manifest.error = error.message.replaceAll(servicePassword, "[redacted]")
  console.error(`operations: FAILED ${manifest.error}`)
  process.exitCode = 1
} finally {
  sampling = false
  if (resourceLoop) await resourceLoop.catch(() => {})
  agent?.destroy()
  manifest.cleanupSucceeded = true
  for (const name of [...new Set(containers)].reverse()) {
    try {
      const result = await execute("docker", ["logs", name], { windowsHide: true, maxBuffer: 32*1024**2, timeout: 30_000 })
      const log = result.stdout+result.stderr
      writeFileSync(join(out, name.replace(id, "reference")+".log"), log.replaceAll(servicePassword, "[redacted]"))
      await docker("rm", "-f", name)
    } catch (error) { manifest.cleanupSucceeded = false; manifest.cleanupError = error.message; process.exitCode = 1 }
  }
  for (const volume of volumes) { try { await docker("volume", "rm", volume) } catch { manifest.cleanupSucceeded = false; process.exitCode = 1 } }
  if (networkCreated) { try { await docker("network", "rm", id) } catch { manifest.cleanupSucceeded = false; process.exitCode = 1 } }
  manifest.source.cleanAfter = !(await git("status", "--porcelain")).trim() && head === (await git("rev-parse", "HEAD")).trim()
  manifest.finishedUTC = new Date().toISOString()
  save("manifest.json", manifest); save("events.json", events); save("resources.json", resources)
  writeFileSync(join(out, "requests.ndjson"), requests.map((r) => JSON.stringify(r)).join("\n")+"\n")
}
if (!process.exitCode) {
  try {
    const report = summarize(manifest, requests, resources, events)
    save("report.json", report); writeFileSync(join(out, "report.md"), markdown(report))
    save("SHA256SUMS.json", Object.fromEntries(["manifest.json", "requests.ndjson", "resources.json", "events.json", "report.json", "backup.dump"].map((file) => [file, sha256(readFileSync(join(out, file)))])))
    writeFileSync(join(root, ".cache/operations", mode === "full" ? "latest.json" : "latest-smoke.json"), JSON.stringify({ directory: out, accepted: report.accepted })+"\n")
    console.log(`operations: PASS ${mode}; accepted=${report.accepted}; ${join(out, "report.md")}`)
  } catch (error) { manifest.error = error.message; save("manifest.json", manifest); console.error(`operations: FAILED ${error.message}`); process.exitCode = 1 }
}
