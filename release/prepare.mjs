import { execFileSync } from "node:child_process"
import { mkdirSync, readFileSync, writeFileSync, mkdtempSync } from "node:fs"
import { join, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { inspectOCI } from "./verify.mjs"
import { sha256 } from "./licenses.mjs"

const root = resolve(fileURLToPath(new URL("..", import.meta.url)))
// Git for Windows may put GNU tar first on PATH; it treats D: as a remote host.
const tar = process.platform === "win32" ? join(process.env.SystemRoot ?? "C:\\Windows", "System32/tar.exe") : "tar"
const run = (cmd, args, cwd = root, extra = {}) => execFileSync(cmd, args, { cwd, stdio: "inherit", ...extra })
const git = (...args) => run("git", args, root, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }).trim()
if (git("status", "--porcelain").length) throw Error("Commit reviewed source before preparing a candidate")
const { version, platform, sbomGenerator } = JSON.parse(readFileSync(join(root, "release/version.json")))
if (!/^v0\.\d+\.\d+-rc\.[1-9]\d*$/.test(version)) throw Error("Expected an explicit prerelease version")
const revision = git("rev-parse", "HEAD"), epoch = git("show", "-s", "--format=%ct", "HEAD")
mkdirSync(join(root, ".cache/releases"), { recursive: true })
const out = mkdtempSync(join(root, ".cache/releases", `${version}-`))
const source = join(out, "source")
mkdirSync(source)
const archive = join(out, "source.tar")
run("git", ["archive", "--format=tar", "-o", archive, "HEAD"])
run(tar, ["-xf", archive, "-C", source])
const report = { version, revision, epoch, platform, sourceSHA256: sha256(readFileSync(archive)), sbomGenerator, unsignedLocalEvidence: true, images: {} }
console.log(`Release output: ${out}`)
for (let pass = 1; pass <= 2; pass++) {
  for (const starter of ["basic", "admin", "rest"]) {
    const name = `${starter}-${pass}`, dest = join(out, `${name}.oci.tar`)
    run("docker", ["buildx", "build", ...(starter === "basic" ? ["--no-cache"] : []), "--platform", platform,
      "-f", "release/Dockerfile", "--target", starter, "--tag", `chitu-rc-${revision.slice(0, 12)}-${starter}:${version}`,
      "--build-arg", `VERSION=${version}`, "--build-arg", `REVISION=${revision}`, "--build-arg", `SOURCE_DATE_EPOCH=${epoch}`,
      "--provenance=mode=max", "--attest", `type=sbom,generator=${sbomGenerator}`,
      "--output", `type=oci,dest=${dest},rewrite-timestamp=true`, "."], source)
    const extracted = join(out, name)
    mkdirSync(extracted)
    run(tar, ["-xf", dest, "-C", extracted])
    const image = inspectOCI(extracted, platform)
    if (!["github.com/chitushka/framework", "stdlib"].every((name) => image.sbom.packages.some((p) => p.name === name))) throw Error("SBOM omits Go runtime/modules")
    if (starter !== "rest" && !image.sbom.packages.some((p) => p.name === "@adminkit/core" && p.versionInfo === "3.5.0")) throw Error("SBOM omits audited frontend")
    if (image.config.config.Labels["org.opencontainers.image.revision"] !== revision || image.config.config.Labels["org.opencontainers.image.version"] !== version) throw Error("Incorrect source/version label")
    if (pass === 1) {
      report.images[starter] = { digest: image.digest, archiveSHA256: sha256(readFileSync(dest)), bytes: readFileSync(dest).length }
      writeFileSync(join(out, `${starter}.spdx.json`), JSON.stringify(image.sbom, null, 2) + "\n")
      writeFileSync(join(out, `${starter}.provenance.json`), JSON.stringify(image.provenance, null, 2) + "\n")
      // Loading is local only. Runtime platforms and attestations remain in OCI.
      run("docker", ["load", "-i", dest])
      run("docker", ["tag", image.digest, `chitu-rc-${revision.slice(0, 12)}-${starter}:${version}`])
      if (starter === "basic") {
        const container = run("docker", ["create", `chitu-rc-${revision.slice(0, 12)}-${starter}:${version}`], root, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }).trim()
        try { run("docker", ["cp", `${container}:/app/licenses/npm/frontend.spdx.json`, join(out, "frontend.spdx.json")]) }
        finally { run("docker", ["rm", container]) }
      }
    } else {
      if (report.images[starter].digest !== image.digest) throw Error(`Non-reproducible ${starter} runtime manifest`)
      report.images[starter].reproduced = true
    }
  }
}
writeFileSync(join(out, "release.json"), JSON.stringify(report, null, 2) + "\n")
const checksums = ["source.tar", "release.json", "frontend.spdx.json", ...["basic", "admin", "rest"].flatMap((s) => [`${s}-1.oci.tar`, `${s}.spdx.json`, `${s}.provenance.json`])]
writeFileSync(join(out, "SHA256SUMS"), checksums.map((file) => `${sha256(readFileSync(join(out, file)))}  ${file}`).join("\n") + "\n")
run("docker", ["run", "--rm", "--mount", `type=bind,source=${out},target=/release`, "-e", "GOMAXPROCS=2", "-e", "GOFLAGS=-p=2", "-e", "GOTOOLCHAIN=local", "-w", "/release/source/tools/releasecheck", "golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e", "go", "run", "-mod=readonly", ".", "-source", "/release/source", "-output", "/release/module-check", "-version", version])
run(process.execPath, [join(root, "release/smoke.mjs"), out])
writeFileSync(join(root, ".cache/releases/latest.txt"), out + "\n")
console.log(`PASS reproducible runtime manifests, SBOM/provenance: ${out}`)
