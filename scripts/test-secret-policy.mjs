// Exercise the real scanner installed by scripts/security.sh.
import assert from "node:assert/strict"
import { spawnSync } from "node:child_process"
import { randomBytes, createHash } from "node:crypto"
import { readFileSync, writeFileSync, mkdtempSync, mkdirSync, rmSync } from "node:fs"
import { resolve, join } from "node:path"
import { tmpdir } from "node:os"

const root = resolve("."), scanner = resolve(process.argv[2] ?? ".cache/security/bin/gitleaks")
const config = join(root, ".gitleaks.toml")
const digest = createHash("sha256").update(readFileSync("release/api-v1.json", "utf8").replaceAll("\r\n", "\n")).digest("hex")
assert.ok(readFileSync(config, "utf8").includes(digest), "checksum classification is stale")
const directory = mkdtempSync(join(tmpdir(), "chitu-secret-policy-"))
try {
  const output = join(tmpdir(), `chitu-secret-results-${randomBytes(6).toString("hex")}.json`)
  const scan = () => {
    const child = spawnSync(scanner, ["dir", "--redact", "--ignore-gitleaks-allow", "--config", config, "--report-format", "json", "--report-path", output, directory], { encoding: "utf8", windowsHide: true, timeout: 30_000 })
    if (child.error) throw child.error
    const findings = JSON.parse(readFileSync(output, "utf8"))
    rmSync(output)
    assert.ok([0, 1].includes(child.status), "scanner did not complete")
    return { status: child.status, findings }
  }
  mkdirSync(join(directory, "application"))
  for (const path of ["release/freeze-v1.json", "docs/releases/v1.0-proposal/compatibility.json", "docs/releases/v1.0-proposal/distribution/release.json"]) {
    const file = join(directory, path), application = join(directory, "application/settings.json")
    mkdirSync(join(file, ".."), { recursive: true })
    const metadata = (value) => JSON.stringify(path.endsWith("distribution/release.json") ? { promotion: value } : value, null, 2)+"\n"
    writeFileSync(file, metadata({ apiSHA256: digest }))
    assert.equal(scan().status, 0, `${path}: public inventory digest was treated as a credential`)
    writeFileSync(file, metadata({ apiSHA256: digest, api_key: randomBytes(40).toString("base64url") }))
    assert.equal(scan().status, 1, `${path}: credential in the same metadata path was ignored`)
    writeFileSync(file, metadata({ api_key: digest }))
    assert.equal(scan().status, 1, `${path}: classification escaped its exact field`)
    writeFileSync(file, metadata({ apiSHA256: randomBytes(32).toString("hex") }))
    assert.equal(scan().status, 1, `${path}: classification escaped its exact checksum`)
    writeFileSync(file, metadata({ apiSHA256: digest }))
    writeFileSync(application, JSON.stringify({ apiSHA256: digest }, null, 2)+"\n")
    assert.equal(scan().status, 1, `${path}: application path was accidentally allowlisted`)
    rmSync(application)
    rmSync(file)
  }
  console.log("secret policy: public digest classified in 3 metadata paths; same-file/other-field/application canaries still blocked")
} finally { rmSync(directory, { recursive: true, force: true }) }
