// Test-only HTTPS proxy: exercises Secure cookies and the production proxy policy.
import { spawn, spawnSync } from "node:child_process"
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import http from "node:http"
import https from "node:https"
import { tmpdir } from "node:os"
import { basename, dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const binary = process.env.BASIC_BINARY ?? join(root, ".cache/basic", process.platform === "win32" ? "basic.exe" : "basic")
const temporaryRoot = resolve(tmpdir())
const directory = mkdtempSync(join(temporaryRoot, "chitu-basic-e2e-"))
const children = []
let proxy
let stopping = false

function check(result) {
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(result.stderr?.toString() || "E2E preparation failed")
  return result.stdout
}

async function cleanup(code = 0) {
  if (stopping) return
  stopping = true
  proxy?.closeAllConnections()
  proxy?.close()
  await Promise.all(children.map((child) => new Promise((done) => {
    if (child.pid === undefined || child.exitCode !== null || child.signalCode !== null) return done()
    const timer = setTimeout(() => child.kill("SIGKILL"), 10_000)
    child.once("exit", () => { clearTimeout(timer); done() })
    child.kill("SIGTERM")
  })))
  const target = resolve(directory)
  if (dirname(target) !== temporaryRoot || !basename(target).startsWith("chitu-basic-e2e-")) throw new Error("Invalid E2E cleanup directory")
  rmSync(target, { recursive: true, force: true })
  process.exitCode = code
}
process.on("SIGINT", () => void cleanup())
process.on("SIGTERM", () => void cleanup())

function start(environment, port, username, hash = "") {
  const child = spawn(binary, [], {
    cwd: root, stdio: "inherit", windowsHide: true,
    env: { ...process.env, APP_ENV: environment, BASIC_ADDRESS: `127.0.0.1:${port}`,
      BASIC_PUBLIC_DIR: join(root, "public"), BASIC_USERNAME: username,
      BASIC_PASSWORD_HASH_FILE: hash,
      BASIC_ALLOWED_HOSTS: environment === "production" ? "127.0.0.1:8182,localhost:8182,127.0.0.1:8443,localhost:8443" : "127.0.0.1:8181,localhost:8181",
      BASIC_TRUSTED_PROXIES: environment === "production" ? "127.0.0.1/32" : "" },
  })
  children.push(child)
  child.on("error", (error) => { console.error(error.message); void cleanup(1) })
  child.on("exit", (code) => { if (!stopping) { console.error(`Basic exited early (${code})`); void cleanup(1) } })
}

async function ready(url) {
  const deadline = Date.now() + 25_000
  let detail = ""
  while (Date.now() < deadline) {
    if (stopping) throw new Error("Basic exited during boot")
    try {
      const response = await fetch(url)
      if (response.ok) { await response.arrayBuffer(); return }
      detail = `${response.status}: ${(await response.text()).slice(0, 120)}`
    } catch (error) { detail = error.message }
    await new Promise((done) => setTimeout(done, 100))
  }
  throw new Error(`Basic did not become ready: ${url} (${detail})`)
}

try {
  const hash = check(spawnSync(binary, ["-hash-password"], { input: "browser-production-password\n", encoding: "utf8", windowsHide: true }))
  const hashFile = join(directory, "password.phc")
  writeFileSync(hashFile, hash, { mode: 0o600 })
  check(spawnSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", join(directory, "key.pem"), "-out", join(directory, "cert.pem"), "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1"], { windowsHide: true }))
  start("production", 8182, "owner", hashFile)
  await ready("http://127.0.0.1:8182/login")
  proxy = https.createServer({ key: readFileSync(join(directory, "key.pem")), cert: readFileSync(join(directory, "cert.pem")) }, (request, response) => {
    const upstream = http.request({ hostname: "127.0.0.1", port: 8182, path: request.url, method: request.method,
      headers: { ...request.headers, "x-forwarded-for": "127.0.0.1", "x-forwarded-proto": "https", "x-forwarded-host": request.headers.host } }, (incoming) => {
      response.writeHead(incoming.statusCode, incoming.headers)
      incoming.pipe(response)
    })
    upstream.on("error", () => { response.writeHead(502); response.end() })
    request.on("aborted", () => upstream.destroy())
    response.on("close", () => upstream.destroy())
    request.pipe(upstream)
  })
  await new Promise((done, reject) => { proxy.once("error", reject); proxy.listen(8443, "127.0.0.1", done) })
  // Playwright's ready URL must only succeed once BOTH environments are ready.
  start("development", 8181, "admin")
  await ready("http://127.0.0.1:8181/login")
} catch (error) {
  console.error(error.message)
  await cleanup(1)
}
