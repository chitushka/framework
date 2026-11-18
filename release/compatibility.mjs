import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { readFileSync, readdirSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs"
import { resolve, join, dirname, relative } from "node:path"
import { fileURLToPath } from "node:url"
import { createHash } from "node:crypto"

export const hash = (value) => createHash("sha256").update(value).digest("hex")
export const normalized = (file) => readFileSync(file, "utf8").replaceAll("\r\n", "\n")
function runtimeText(file) {
  let text = normalized(file)
  if (file.endsWith("go.mod")) text = text.replace(/(github\.com\/chitushka\/framework(?:\/adapters)?\s+)(v[^\s]+)/g, (_match, path, version) => {
    assert.ok(["v0.1.0-rc.1", "v1.0.0"].includes(version), "unreviewed framework dependency version")
    return path+"<framework-release>"
  })
  // Owned module checksums change during promotion; external module checksums
  // and every other byte remain part of the runtime fingerprint.
  if (file.endsWith("go.sum")) text = text.split("\n").filter((line) => !/^github\.com\/chitushka\/framework(?:\/(?:adapters|examples\/advanced-admin))? v(?:0\.1\.0-rc\.1|1\.0\.0)(?:\/go.mod)? /.test(line)).join("\n")
  return text
}
export function runtimeFingerprint(root, api) {
  const directories = Object.keys(api.packages).map((path) => path.replace("github.com/chitushka/framework/", "")).concat("internal/httpwriter")
  const files = [...new Set(directories.flatMap((directory) => readdirSync(join(root, directory)).filter((name) => name.endsWith(".go") && !name.endsWith("_test.go")).map((name) => directory+"/"+name)).concat("go.mod", "go.sum", "adapters/go.mod", "adapters/go.sum"))].sort()
  const fingerprints = {}
  for (const name of files) fingerprints[name] = hash(runtimeText(join(root, name)))
  return { files: files.length, sha256: hash(JSON.stringify(fingerprints)), fingerprints }
}
export function compareRuntime(current, reference, api) {
  const expected = runtimeFingerprint(reference, api), actual = runtimeFingerprint(current, api)
  assert.deepEqual(actual.fingerprints, expected.fingerprints, "runtime/dependency drift from accepted RC")
  return { files: expected.files, sha256: expected.sha256 }
}

export function run(root) {
  const freeze = JSON.parse(readFileSync(join(root, "release/freeze-v1.json")))
  const api = JSON.parse(readFileSync(join(root, "release/api-v1.json")))
  assert.equal(hash(normalized(join(root, "release/api-v1.json"))), freeze.apiSHA256, "unreviewed API snapshot replacement")
  mkdirSync(join(root, ".cache/compatibility"), { recursive: true })
  const output = mkdtempSync(join(root, ".cache/compatibility/run-"))
  const execute = (cmd, args, options = {}) => execFileSync(cmd, args, { cwd: root, encoding: "utf8", maxBuffer: 64*1024**2, timeout: 600_000, windowsHide: true, stdio: ["ignore", "pipe", "pipe"], ...options })
  const report = { schema: 1, candidate: freeze.candidate, apiSHA256: freeze.apiSHA256, sourceCommit: execute("git", ["rev-parse", "HEAD"]).trim(), cleanTree: !execute("git", ["status", "--porcelain"]).trim(), startedUTC: new Date().toISOString(), passed: false }
  try {
    const archive = join(output, "source.tar"), source = join(output, "source")
    mkdirSync(source)
    execute("git", ["-c", "core.autocrlf=true", "archive", "--format=tar", "-o", archive, freeze.candidate.revision])
    assert.equal(hash(readFileSync(archive)), freeze.candidate.sourceSHA256, "accepted RC archive differs")
    const tar = process.platform === "win32" ? join(process.env.SystemRoot ?? "C:\\Windows", "System32/tar.exe") : "tar"
    execute(tar, ["-xf", archive, "-C", source])
    report.runtime = compareRuntime(root, source, api)
    const binary = join(output, process.platform === "win32" ? "apifreeze.exe" : "apifreeze")
    execute("go", ["build", "-o", binary, "./tools/apifreeze"])
    report.currentAPI = execute(binary, ["-root", root]).trim()
    report.referenceAPI = execute(binary, ["-root", source, "-baseline", join(root, "release/api-v1.json")]).trim()
    const fixtures = readdirSync(join(root, "compatibility")).filter((name) => name.endsWith("_test.go")).sort()
    assert.ok(fixtures.length > 0, "consumer fixtures missing")
    report.fixtureSHA256 = hash(fixtures.map((name) => normalized(join(root, "compatibility", name))).join("\n"))
    const log = execute("go", ["-C", join(root, "tools/releasecheck"), "run", "-mod=readonly", ".", "-source", source, "-output", join(output, "consumer-check"), "-version", freeze.candidate.version, "-compatibility", join(root, "compatibility")])
    writeFileSync(join(output, "consumer.log"), log)
    assert.match(log, /ok\s+example.com\/rc-consumer/, "installed consumer tests did not run")
    report.versionTagConsumer = true
    report.dependencyVerification = true
    report.adminBuild = true
    report.passed = true
    return { output, report }
  } catch (error) {
    report.error = error.message
    throw error
  } finally {
    report.finishedUTC = new Date().toISOString()
    writeFileSync(join(output, "report.json"), JSON.stringify(report, null, 2)+"\n")
    console.log(`compatibility evidence: ${relative(root, output)}`)
  }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = resolve(fileURLToPath(new URL("..", import.meta.url)))
  const { report } = run(root)
  console.log(`PASS ${report.runtime.files} runtime/dependency files, current/frozen API, external version-tag consumer and Admin build`)
}
