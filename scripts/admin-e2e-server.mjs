// Private test harness. Restart control is NEVER part of the application binary.
import { spawn, spawnSync } from "node:child_process"
import { mkdtempSync, rmSync, writeFileSync } from "node:fs"
import http from "node:http"
import { tmpdir } from "node:os"
import { basename, dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const binary = process.env.ADMIN_BINARY ?? join(root, ".cache/admin", process.platform === "win32" ? "admin.exe" : "admin")
const temporaryRoot = resolve(tmpdir())
const directory = mkdtempSync(join(temporaryRoot, "chitu-admin-e2e-"))
const environment = { ...process.env, APP_ENV: "development", ADMIN_ADDRESS: "127.0.0.1:8281", ADMIN_PUBLIC_DIR: join(root, "public"), ADMIN_ALLOWED_HOSTS: "127.0.0.1:8281,localhost:8281", ADMIN_TRUSTED_PROXIES: "" }
let child, control, stopping = false, restarting = false

function checked(args, input) {
  const result = spawnSync(binary, args, { cwd: root, env: environment, input, encoding: "utf8", windowsHide: true })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(result.stderr || "Admin setup failed")
  return result.stdout
}
async function stopChild() {
  if (!child || child.pid === undefined || child.exitCode !== null || child.signalCode !== null) return
  const current = child
  await new Promise((done) => {
    const timer = setTimeout(() => current.kill("SIGKILL"), 13_000)
    current.once("exit", () => { clearTimeout(timer); done() })
    current.kill("SIGTERM")
  })
}
async function cleanup(code = 0) {
  if (stopping) return
  stopping = true
  control?.closeAllConnections(); control?.close()
  await stopChild()
  const target = resolve(directory)
  if (dirname(target) !== temporaryRoot || !basename(target).startsWith("chitu-admin-e2e-")) throw new Error("Invalid cleanup directory")
  rmSync(target, { recursive: true, force: true })
  process.exitCode = code
}
process.on("SIGTERM", () => void cleanup())
process.on("SIGINT", () => void cleanup())

async function startChild() {
  child = spawn(binary, [], { cwd: root, env: environment, stdio: "inherit", windowsHide: true })
  child.on("error", (error) => { console.error(error.message); void cleanup(1) })
  child.on("exit", (code) => { if (!stopping && !restarting) { console.error(`Admin exited unexpectedly: ${code}`); void cleanup(1) } })
  const deadline = Date.now() + 25_000
  let detail = ""
  while (Date.now() < deadline) {
    if (stopping || child.exitCode !== null) throw new Error("Admin exited during boot")
    try { const response = await fetch("http://127.0.0.1:8281/login"); detail = await response.text(); if (response.ok) return } catch (error) { detail = error.message }
    await new Promise((done) => setTimeout(done, 100))
  }
  throw new Error(`Admin not ready: ${detail.slice(0, 200)}`)
}

try {
  for (const [account, password] of [["ADMIN", "admin-e2e-password"], ["EDITOR", "editor-e2e-password"]]) {
    const hash = checked(["-hash-password"], password + "\n")
    const path = join(directory, `${account}.phc`)
    writeFileSync(path, hash, { mode: 0o600 })
    environment[`ADMIN_SEED_${account}_HASH_FILE`] = path
  }
  checked(["-migrate"])
  checked(["-seed"])
  control = http.createServer(async (request, response) => {
    if (request.method !== "POST" || request.url !== "/restart") { response.writeHead(404); response.end(); return }
    if (restarting) { response.writeHead(409); response.end(); return }
    restarting = true
    try { await stopChild(); await startChild(); response.writeHead(204); response.end() }
    catch (error) { console.error(error.message); response.writeHead(500); response.end(); void cleanup(1) }
    finally { restarting = false }
  })
  await new Promise((done, reject) => { control.once("error", reject); control.listen(8289, "127.0.0.1", done) })
  await startChild()
} catch (error) { console.error(error.message); await cleanup(1) }
