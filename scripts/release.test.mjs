import assert from "node:assert/strict"
import test from "node:test"
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { inspectOCI } from "../release/verify.mjs"
import { selectAssets } from "../release/assets.mjs"
import { sha256, frontendNotices } from "../release/licenses.mjs"

function fixture(t, change = {}) {
  const dir = mkdtempSync(join(tmpdir(), "chitu-oci-"))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, "blobs/sha256"), { recursive: true })
  const put = (value, mediaType = "application/vnd.oci.image.manifest.v1+json") => {
    const data = Buffer.from(JSON.stringify(value)), hash = sha256(data)
    writeFileSync(join(dir, "blobs/sha256", hash), data)
    return { digest: "sha256:" + hash, size: data.length, mediaType }
  }
  const config = put({ config: { User: change.user ?? "65532:65532" } })
  const image = { ...put({ config, layers: [] }), platform: { os: "linux", architecture: "amd64" } }
  const statement = (predicateType, predicate) => put({ subject: [{ digest: { sha256: change.subject ?? image.digest.slice(7) } }], predicateType, predicate })
  const layers = [statement("https://spdx.dev/Document", { spdxVersion: "SPDX-2.3", packages: [{ name: "fixture" }] })]
  if (!change.omitProvenance) layers.push(statement("https://slsa.dev/provenance/v0.2", { buildType: "fixture-builder" }))
  const attestation = { ...put({ config: put({}), layers }), platform: { os: "unknown", architecture: "unknown" }, annotations: { "vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": image.digest } }
  writeFileSync(join(dir, "index.json"), JSON.stringify({ manifests: [image, attestation] }))
  return { dir, config }
}

test("artifact gate rejects tampering, wrong subject, absent provenance and root runtime", (t) => {
  assert.match(inspectOCI(fixture(t).dir).digest, /^sha256:/)
  for (const change of [{ subject: "a".repeat(64) }, { omitProvenance: true }, { user: "0" }]) assert.throws(() => inspectOCI(fixture(t, change).dir))
  const { dir, config } = fixture(t)
  writeFileSync(join(dir, "blobs/sha256", config.digest.slice(7)), "tampered")
  assert.throws(() => inspectOCI(dir), /Corrupted OCI blob/)
})

test("starter assets exclude unrelated entries and reject traversal", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "chitu-assets-"))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, ".vite"))
  mkdirSync(join(dir, "assets"))
  const manifest = { "src/basic.js": { file: "assets/basic.js", css: ["assets/shared.css"] }, "src/demo.js": { file: "assets/missing-demo.js" } }
  for (const file of ["basic.js", "shared.css"]) writeFileSync(join(dir, "assets", file), "fixture")
  writeFileSync(join(dir, ".vite/manifest.json"), JSON.stringify(manifest))
  assert.doesNotThrow(() => selectAssets(dir, join(dir, "out"), "src/basic.js"))
  manifest["src/basic.js"].file = "assets/../../secret"
  writeFileSync(join(dir, ".vite/manifest.json"), JSON.stringify(manifest))
  assert.throws(() => selectAssets(dir, join(dir, "out2"), "src/basic.js"), /Unsafe asset/)
})

test("license gate preserves upstream notices and rejects changed or unreviewed assets", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "chitu-notices-"))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const vendor = join(dir, "frontend/node_modules/@adminkit/core")
  mkdirSync(join(vendor, "dist/js"), { recursive: true })
  mkdirSync(join(vendor, "dist/css"), { recursive: true })
  mkdirSync(join(dir, "release"))
  const files = { "LICENSE": "upstream MIT text", "dist/js/app.js.LICENSE.txt": "bundled notices", "dist/css/app.css": "body {}", "dist/js/app.js": "fixture" }
  for (const [file, data] of Object.entries(files)) writeFileSync(join(vendor, file), data)
  writeFileSync(join(vendor, "package.json"), JSON.stringify({ name: "@adminkit/core", version: "3.5.0" }))
  const lock = { packages: { "node_modules/@adminkit/core": { version: "3.5.0", license: "MIT", integrity: "sha512-fixture", resolved: "https://example.test/vendor.tgz" } } }
  writeFileSync(join(dir, "frontend/package-lock.json"), JSON.stringify(lock))
  writeFileSync(join(dir, "release/frontend-audit.json"), JSON.stringify({ sha256: Object.fromEntries(Object.entries(files).map(([file, data]) => [file, sha256(data)])) }))
  const out = join(dir, "out")
  frontendNotices(dir, out)
  assert.equal(readFileSync(join(out, "AdminKit-bundled-JS-LICENSE.txt"), "utf8"), "bundled notices")
  const spdx = JSON.parse(readFileSync(join(out, "frontend.spdx.json")))
  assert.equal(spdx.packages[0].licenseDeclared, "MIT")
  lock.packages["node_modules/@adminkit/core"].license = "UNREVIEWED"
  writeFileSync(join(dir, "frontend/package-lock.json"), JSON.stringify(lock))
  assert.throws(() => frontendNotices(dir, out), /Review required/)
  writeFileSync(join(vendor, "dist/js/app.js"), "changed vendor")
  assert.throws(() => frontendNotices(dir, out), /Re-audit changed/)
})
