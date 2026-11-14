import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { randomBytes, generateKeyPairSync, sign } from "node:crypto"
import { readFileSync, writeFileSync, mkdirSync } from "node:fs"
import { resolve, join } from "node:path"
import { request as httpRequest } from "node:http"

const output = resolve(process.argv[2] ?? "")
const report = JSON.parse(readFileSync(join(output, "release.json"), "utf8"))
const version = report.version, revision = report.revision
const id = `chitu-smoke-${randomBytes(6).toString("hex")}`
const secrets = join(output, id), containers = []
mkdirSync(secrets)
const docker = (...args) => execFileSync("docker", args, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], windowsHide: true }).trim()
const image = (starter) => report.images[starter].indexDigest
const password = randomBytes(24).toString("hex"), sqlPassword = randomBytes(24).toString("hex")
const hash = execFileSync("docker", ["run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "-i", image("basic"), "-hash-password"], { input: password + "\n", encoding: "utf8" }).trim()
writeFileSync(join(secrets, "password.phc"), hash + "\n", { mode: 0o444 })
const { publicKey, privateKey } = generateKeyPairSync("ed25519")
writeFileSync(join(secrets, "issuer.pem"), publicKey.export({ type: "spki", format: "pem" }), { mode: 0o444 })
const b64 = (value) => Buffer.from(JSON.stringify(value)).toString("base64url")
const now = Math.floor(Date.now() / 1000)
const body = `${b64({ alg: "EdDSA", typ: "at+jwt", kid: "rc" })}.${b64({ sub: "smoke", iss: "rc-issuer", aud: "rc-api", iat: now, exp: now + 600 })}`
const token = body + "." + sign(null, Buffer.from(body), privateKey).toString("base64url")
const delay = () => new Promise((done) => setTimeout(done, 500))
function options(name, env = {}) {
  return ["--name", name, "--network", id, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--mount", `type=bind,source=${secrets},target=/run/secrets,readonly`, ...Object.entries(env).flatMap(([key, value]) => ["-e", `${key}=${value}`])]
}
async function request(base, path, options = {}) {
  // Native HTTP preserves the explicit test Host independently of fetch's
  // forbidden-header policy. Redirects and Secure cookies are handled manually.
  return new Promise((resolve, reject) => {
    const req = httpRequest(base + path, { method: options.method ?? "GET", headers: { host: "rc.test", ...options.headers }, signal: AbortSignal.timeout(3000) }, (res) => {
      const chunks = []; let bytes = 0
      res.on("data", (chunk) => { bytes += chunk.length; if (bytes > 2 * 1024 * 1024) res.destroy(Error("Smoke response exceeds limit")); else chunks.push(chunk) })
      res.on("error", reject)
      res.on("end", () => {
        const headers = new Headers()
        for (let i = 0; i < res.rawHeaders.length; i += 2) headers.append(res.rawHeaders[i], res.rawHeaders[i + 1])
        resolve(new Response([204, 205, 304].includes(res.statusCode) ? null : Buffer.concat(chunks), { status: res.statusCode, headers }))
      })
    })
    req.on("error", reject)
    req.end(options.body?.toString())
  })
}
async function start(starter, port, env) {
  const name = `${id}-${starter}`
  containers.push(name)
  docker("run", "-d", ...options(name, env), "-p", `127.0.0.1::${port}`, image(starter))
  const address = docker("port", name, `${port}/tcp`)
  const base = `http://${address}`
  let last = "no response"
  for (const deadline = Date.now() + 45000; Date.now() < deadline;) {
    let ready = false
    try {
      const r = await request(base, starter === "rest" ? "/openapi.json" : "/login")
      ready = r.status === 200
      last = `HTTP ${r.status}`
    } catch (err) { last = err.message }
    if (ready) {
      const container = JSON.parse(docker("inspect", name))[0]
      assert.equal(container.Config.User, "65532:65532")
      assert.equal(container.HostConfig.ReadonlyRootfs, true)
      // Do not trust a mutable local tag to select the verified runtime.
      assert.equal(container.Image, image(starter))
      return { name, base }
    }
    await delay()
  }
  throw Error(`Runtime did not become ready: ${name} (${last})\n${docker("logs", name)}`)
}
async function login(base, dashboard) {
  let r = await request(base, "/login")
  let html = await r.text()
  const cookie = r.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ")
  assert.match(r.headers.getSetCookie().join(";"), /Secure/)
  assert.doesNotMatch(html, /Локальный пример/)
  const csrf = html.match(/name="_csrf" value="([^"]+)"/)?.[1]
  assert.ok(csrf)
  for (const path of [...html.matchAll(/(?:href|src)="(\/assets\/[^"?#]+)"/g)].map((m) => m[1])) assert.equal((await request(base, path)).status, 200)
  r = await request(base, "/login", { method: "POST", headers: { cookie, "content-type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ username: "admin", password, _csrf: csrf }) })
  assert.equal(r.status, 303)
  const authenticated = r.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ")
  r = await request(base, dashboard, { headers: { cookie: authenticated || cookie } })
  assert.equal(r.status, 200)
  html = await r.text()
  assert.match(html, /admin|Администратор|Панель/)
  return authenticated || cookie
}
try {
  docker("network", "create", id)
  const db = `${id}-postgres`
  containers.push(db)
  docker("run", "-d", "--name", db, "--network", id, "--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "-e", "POSTGRES_USER=chitu", "-e", `POSTGRES_PASSWORD=${sqlPassword}`, "-e", "POSTGRES_DB=chitu", "postgres:17-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b")
  let ready = false
  for (let i = 0; i < 90; i++) {
    try { docker("exec", db, "pg_isready", "-U", "chitu", "-d", "chitu"); ready = true; break } catch { await delay() }
  }
  assert.ok(ready, "PostgreSQL readiness")
  const basic = await start("basic", 8080, { BASIC_USERNAME: "admin", BASIC_PASSWORD_HASH_FILE: "/run/secrets/password.phc", BASIC_ALLOWED_HOSTS: "rc.test" })
  await login(basic.base, "/dashboard")
  const adminEnv = { ADMIN_ALLOWED_HOSTS: "rc.test", ADMIN_DB_DRIVER: "pgx", ADMIN_DB_DSN: `postgres://chitu:${sqlPassword}@${db}:5432/chitu?sslmode=disable`, ADMIN_SEED_ADMIN_HASH_FILE: "/run/secrets/password.phc", ADMIN_SEED_EDITOR_HASH_FILE: "/run/secrets/password.phc" }
  for (const flag of ["-migrate", "-seed"]) docker("run", "--rm", ...options(`${id}-setup`, adminEnv), image("admin"), flag)
  const admin = await start("admin", 8081, adminEnv)
  await login(admin.base, "/admin")
  const rest = await start("rest", 8082, { REST_ALLOWED_HOSTS: "rc.test", JWT_PUBLIC_KEY_FILE: "/run/secrets/issuer.pem", JWT_KEY_ID: "rc", JWT_ISSUER: "rc-issuer", JWT_AUDIENCE: "rc-api" })
  assert.equal((await request(rest.base, "/v1/articles")).status, 401)
  const headers = { authorization: `Bearer ${token}`, "content-type": "application/json" }
  let r = await request(rest.base, "/v1/articles", { method: "POST", headers, body: JSON.stringify({ title: "RC smoke" }) })
  assert.equal(r.status, 201)
  const location = r.headers.get("location")
  assert.match(location, /^\/v1\/articles\/\d+$/)
  assert.equal((await request(rest.base, location, { headers })).status, 200)
  assert.equal((await request(rest.base, location, { method: "PUT", headers, body: JSON.stringify({ title: "Updated RC" }) })).status, 200)
  assert.equal((await request(rest.base, location, { method: "DELETE", headers })).status, 204)
  for (const runtime of [basic, admin, rest]) {
    docker("stop", "-t", "15", runtime.name)
    assert.equal(JSON.parse(docker("inspect", runtime.name))[0].State.ExitCode, 0, "SIGTERM shutdown")
  }
  writeFileSync(join(output, "smoke.json"), JSON.stringify({ revision, version, production: true, nonRoot: true, readOnly: true, basicLoginAssets: true, adminPostgreSQLMigrationSeedLogin: true, restExternalJWTCRUD: true, sigtermExitZero: true }, null, 2) + "\n")
  console.log("PASS RC production runtime smoke (Basic/Admin/REST)")
} finally {
  for (const name of containers.reverse()) docker("rm", "-f", name)
  docker("network", "rm", id)
}
