import assert from "node:assert/strict"
import { readFileSync, existsSync } from "node:fs"
import { gunzipSync } from "node:zlib"
import { join, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { sha256, summarize } from "./report.mjs"

export function verify(directory) {
  const read = (name) => existsSync(join(directory, name)) ? readFileSync(join(directory, name)) : gunzipSync(readFileSync(join(directory, name+".gz")))
  const names = ["manifest.json", "requests.ndjson", "resources.json", "events.json", "report.json", "backup.dump"]
  const sums = JSON.parse(read("SHA256SUMS.json"))
  assert.deepEqual(Object.keys(sums).sort(), [...names].sort(), "unexpected/missing artifact checksums")
  const data = Object.fromEntries(names.map((name) => {
    const bytes = read(name)
    assert.equal(sha256(bytes), sums[name], `artifact checksum: ${name}`)
    return [name, bytes]
  }))
  const report = summarize(JSON.parse(data["manifest.json"]), data["requests.ndjson"].toString().trim().split("\n").map((line) => JSON.parse(line)), JSON.parse(data["resources.json"]), JSON.parse(data["events.json"]))
  assert.deepEqual(JSON.parse(data["report.json"]), report, "report differs from reconstructed observations")
  assert.equal(report.events.find((e) => e.name === "backup-restore").dumpSHA256, sums["backup.dump"], "backup differs from restored artifact")
  return report
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const report = verify(resolve(process.argv[2] ?? "."))
  console.log(`PASS independently reconstructed ${report.mode}; accepted=${report.accepted}; ${report.total.requests} requests`)
}
