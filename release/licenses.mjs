import { createHash } from "node:crypto"
import { execFileSync } from "node:child_process"
import { readFileSync, readdirSync, mkdirSync, writeFileSync, copyFileSync, existsSync } from "node:fs"
import { resolve, join } from "node:path"
import { fileURLToPath } from "node:url"

export const sha256 = (data) => createHash("sha256").update(data).digest("hex")
const json = (file) => JSON.parse(readFileSync(file, "utf8"))
const names = (dir) => readdirSync(dir).filter((name) => /^(licen[cs]e|copying|copyright|notice|patents)(\.|$)/i.test(name)).sort()

export function frontendNotices(root, out) {
  mkdirSync(out, { recursive: true })
  const lock = json(join(root, "frontend/package-lock.json"))
  const reviewed = json(join(root, "release/frontend-audit.json"))
  for (const [name, expected] of Object.entries(reviewed.sha256)) {
    if (sha256(readFileSync(join(root, "frontend/node_modules/@adminkit/core", name))) !== expected) throw Error(`Re-audit changed AdminKit asset: ${name}`)
  }
  const inventory = []
  for (const [path, pkg] of Object.entries(lock.packages).sort(([a], [b]) => a.localeCompare(b, "en"))) {
    if (!path || pkg.dev) continue
    if (!["MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC"].includes(pkg.license)) throw Error(`Review required for ${path}: ${pkg.license}`)
    const source = join(root, "frontend", path)
    if (json(join(source, "package.json")).version !== pkg.version || !pkg.integrity) throw Error(`Run npm ci: ${path}`)
    const files = names(source)
    if (!files.length) throw Error(`Missing license: ${path}`)
    const target = path.replace(/^node_modules\//, "").replaceAll("/", "__")
    mkdirSync(join(out, target), { recursive: true })
    for (const file of files) copyFileSync(join(source, file), join(out, target, file))
    inventory.push({ package: path, name: json(join(source, "package.json")).name, version: pkg.version, license: pkg.license, integrity: pkg.integrity, resolved: pkg.resolved, notices: files })
  }
  copyFileSync(join(root, "frontend/node_modules/@adminkit/core/dist/js/app.js.LICENSE.txt"), join(out, "AdminKit-bundled-JS-LICENSE.txt"))
  const css = readFileSync(join(root, "frontend/node_modules/@adminkit/core/dist/css/app.css"), "utf8")
  if (/@import\b/i.test(css) || [...css.matchAll(/url\(\s*["']?([^)]*)/g)].some((m) => !m[1].startsWith("data:image/svg+xml"))) throw Error("Review CSS external/font/image assets")
  const lockSHA256 = sha256(readFileSync(join(root, "frontend/package-lock.json")))
  writeFileSync(join(out, "npm-inventory.json"), JSON.stringify({ lockSHA256, packages: inventory }, null, 2) + "\n")
  const created = new Date(Number(process.env.SOURCE_DATE_EPOCH ?? 0) * 1000).toISOString().replace(".000Z", "Z")
  const packages = inventory.map((pkg) => ({ SPDXID: "SPDXRef-Npm-" + sha256(pkg.package), name: pkg.name, versionInfo: pkg.version, downloadLocation: pkg.resolved, filesAnalyzed: false, licenseDeclared: pkg.license, licenseConcluded: "NOASSERTION", copyrightText: "NOASSERTION" }))
  const spdx = { spdxVersion: "SPDX-2.3", dataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT", name: "Chitu frontend production dependency closure (supplement)", documentNamespace: "https://spdx.org/spdxdocs/chitu-frontend-" + sha256(lockSHA256 + created + JSON.stringify(reviewed)), creationInfo: { creators: ["Tool: chitu-release-license-audit"], created }, packages, relationships: packages.map((p) => ({ spdxElementId: "SPDXRef-DOCUMENT", relatedSpdxElement: p.SPDXID, relationshipType: "DESCRIBES" })) }
  writeFileSync(join(out, "frontend.spdx.json"), JSON.stringify(spdx, null, 2) + "\n")
  return inventory
}

export function goNotices(root, out) {
  mkdirSync(out, { recursive: true })
  const inventory = new Map()
  for (const [cwd, packages] of [[root, ["./examples/basic", "./examples/rest-api"]], [join(root, "examples/advanced-admin"), ["."]]]) {
    const paths = [...new Set(execFileSync("go", ["list", "-mod=readonly", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", ...packages], { cwd, encoding: "utf8" }).trim().split(/\s+/))]
    const stream = execFileSync("go", ["list", "-mod=readonly", "-m", "-json", ...paths], { cwd, encoding: "utf8" })
    // go list emits a sequence of JSON objects, never a JSON array.
    for (const item of JSON.parse("[" + stream.trim().replace(/}\s*{/g, "},{") + "]")) {
      if (item.Main || item.Replace?.Dir) continue
      if (!item.Dir || !item.Sum) throw Error(`Unverified module ${item.Path}`)
      const files = names(item.Dir)
      if (!files.length) throw Error(`Missing Go license: ${item.Path}`)
      const name = item.Path.replaceAll("/", "__") + "@" + item.Version
      mkdirSync(join(out, name), { recursive: true })
      for (const file of files) copyFileSync(join(item.Dir, file), join(out, name, file))
      inventory.set(item.Path, { path: item.Path, version: item.Version, sum: item.Sum, notices: files })
    }
  }
  const goroot = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim()
  copyFileSync(join(goroot, "LICENSE"), join(out, "Go-LICENSE"))
  if (existsSync(join(goroot, "PATENTS"))) copyFileSync(join(goroot, "PATENTS"), join(out, "Go-PATENTS"))
  writeFileSync(join(out, "go-inventory.json"), JSON.stringify([...inventory.values()].sort((a, b) => a.path.localeCompare(b.path, "en")), null, 2) + "\n")
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [kind, output] = process.argv.slice(2)
  if (!output || !["frontend", "go"].includes(kind)) throw Error("Usage: node release/licenses.mjs frontend|go OUTPUT")
  ;(kind === "frontend" ? frontendNotices : goNotices)(process.cwd(), resolve(output))
}
