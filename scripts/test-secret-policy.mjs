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
  mkdirSync(join(directory, "release"))
  const file = join(directory, "release/freeze-v1.json"), output = join(tmpdir(), `chitu-secret-results-${randomBytes(6).toString("hex")}.json`)
  const scan = () => {
    const child = spawnSync(scanner, ["dir", "--redact", "--ignore-gitleaks-allow", "--config", config, "--report-format", "json", "--report-path", output, directory], { encoding: "utf8", windowsHide: true, timeout: 30_000 })
    if (child.error) throw child.error
    const findings = JSON.parse(readFileSync(output, "utf8"))
    rmSync(output)
    assert.ok([0, 1].includes(child.status), "scanner did not complete")
    return { status: child.status, findings }
  }
  writeFileSync(file, JSON.stringify({ apiSHA256: digest }, null, 2)+"\n")
  assert.equal(scan().status, 0, "public inventory digest was treated as a credential")
  writeFileSync(file, JSON.stringify({ apiSHA256: digest, api_key: randomBytes(40).toString("base64url") }, null, 2)+"\n")
  assert.equal(scan().status, 1, "credential in the same metadata path was ignored")
  writeFileSync(file, JSON.stringify({ api_key: digest }, null, 2)+"\n")
  assert.equal(scan().status, 1, "classification escaped its exact field")
  writeFileSync(file, JSON.stringify({ apiSHA256: digest }, null, 2)+"\n")
  mkdirSync(join(directory, "application"))
  writeFileSync(join(directory, "application/settings.json"), JSON.stringify({ apiSHA256: digest }, null, 2)+"\n")
  assert.equal(scan().status, 1, "application path was accidentally allowlisted")
  console.log("secret policy: public digest classified; same-file/other-field/application canaries still blocked")
} finally { rmSync(directory, { recursive: true, force: true }) }
