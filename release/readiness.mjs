// This command checks evidence and reports blockers. It never publishes or signs.
import { readFileSync, existsSync } from "node:fs"
import { execFileSync } from "node:child_process"
import { resolve, join } from "node:path"
import { fileURLToPath } from "node:url"
import { gunzipSync } from "node:zlib"
import { hash, normalized, runtimeFingerprint } from "./compatibility.mjs"
import { verify } from "./operations/verify.mjs"

export function externalBlockers(freeze, approval, head, now = new Date()) {
  const blockers = []
  if (!approval || approval.schema !== 1) return ["hosted CI evidence, required-check configuration and owner signoff are missing"]
  if (approval.sourceCommit !== head || !/^[a-f0-9]{40}$/.test(head)) blockers.push("approval is for a different source commit")
  if (approval.targetVersion !== freeze.targetVersion || approval.apiSHA256 !== freeze.apiSHA256 || approval.candidateRevision !== freeze.candidate.revision) blockers.push("approval does not bind the target version, API and accepted RC")
  const ci = approval.hostedCI
  if (ci?.repository !== freeze.repository || ci?.headSHA !== head || ci?.conclusion !== "success" || !new RegExp(`^https://github\\.com/${freeze.repository}/actions/runs/[0-9]+$`).test(ci?.runURL ?? "")) blockers.push("hosted CI success for this source is missing")
  for (const name of [...freeze.mandatoryJobs, "quality-gate"]) {
    const children = ci?.jobs?.[name]
    const count = name === "test" || name === "lint" ? 4 : 1
    if (!Array.isArray(children) || children.length !== count || children.some((job) => !job.name || job.conclusion !== "success") || new Set(children.map((j) => j.name)).size !== count) blockers.push(`hosted job/matrix evidence incomplete: ${name}`)
  }
  for (const name of freeze.requiredArtifacts) {
    if (!ci?.artifacts?.some((artifact) => artifact.name === name && Number.isSafeInteger(artifact.id) && artifact.id > 0 && artifact.expired === false)) blockers.push(`hosted artifact missing/expired: ${name}`)
  }
  const protection = approval.requiredChecks
  if (protection?.repository !== freeze.repository || protection?.branch !== "master" || !protection?.contexts?.includes(freeze.requiredCheck) || protection?.strict !== true || protection?.enforceAdmins !== true || protection?.bypassRestricted !== true) blockers.push("required quality check and bypass/release restrictions are not confirmed")
  const owner = approval.ownerSignoff
  const when = Date.parse(owner?.approvedUTC ?? "")
  if (owner?.login !== freeze.repository.split("/")[0] || !Number.isFinite(when) || when > now.getTime() || owner?.apiReviewed !== true || owner?.upgradeReviewed !== true || owner?.noOpenP0Confirmed !== true || owner?.securityGatesReviewed !== true || owner?.checklistSHA256 !== hash(JSON.stringify({ targetVersion: freeze.targetVersion, apiSHA256: freeze.apiSHA256, candidate: freeze.candidate, sourceCommit: head }))) blockers.push("owner has not signed the exact API/upgrade/security/release checklist")
  if (approval.publicationPermission?.sourceCommit !== head || approval.publicationPermission?.targetVersion !== freeze.targetVersion || approval.publicationPermission?.explicitlyAuthorized !== true) blockers.push("separate authorization to publish tags/artifacts is missing")
  return blockers
}

export function assess(root, approvalFile) {
  const freeze = JSON.parse(readFileSync(join(root, "release/freeze-v1.json")))
  const head = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8", windowsHide: true }).trim()
  const checks = {}, blockers = []
  const check = (name, action) => { try { action(); checks[name] = "passed" } catch (error) { checks[name] = "failed"; blockers.push(`${name}: ${error.message}`) } }
  check("API baseline", () => {
    if (hash(normalized(join(root, "release/api-v1.json"))) !== freeze.apiSHA256) throw Error("unreviewed snapshot replacement")
    execFileSync("go", ["run", "./tools/apifreeze"], { cwd: root, encoding: "utf8", timeout: 180_000, windowsHide: true })
  })
  check("operational RC", () => {
    const report = verify(join(root, freeze.operationalEvidence))
    if (!report.accepted || report.candidate.revision !== freeze.candidate.revision || report.candidate.sourceSHA256 !== freeze.candidate.sourceSHA256) throw Error("operational evidence is for a different RC")
  })
  check("compatibility and runtime identity", () => {
    const report = JSON.parse(readFileSync(join(root, "docs/releases/v1.0-proposal/compatibility.json")))
    const api = JSON.parse(readFileSync(join(root, "release/api-v1.json")))
    if (report.passed !== true || report.versionTagConsumer !== true || report.dependencyVerification !== true || report.adminBuild !== true || report.apiSHA256 !== freeze.apiSHA256 || report.candidate.revision !== freeze.candidate.revision || report.runtime.sha256 !== runtimeFingerprint(root, api).sha256) throw Error("consumer/runtime acceptance missing or stale")
    const fixture = hash(normalized(join(root, "compatibility/public_test.go")))
    if (report.fixtureSHA256 !== fixture || freeze.compatibilityFixtureSHA256 !== fixture) throw Error("consumer fixture changed since acceptance")
  })
  check("known P0 disposition", () => {
    if (freeze.p0.length !== 4 || freeze.p0.some((item) => item.status !== "closed" || !existsSync(join(root, item.test)) || !existsSync(join(root, item.evidence)))) throw Error("known local P0 is open or lacks evidence")
  })
  check("security policy and local validation", () => {
    const exceptions = JSON.parse(readFileSync(join(root, ".github/security-exceptions.json")))
    const local = JSON.parse(readFileSync(join(root, "docs/releases/v1.0-proposal/local-validation.json")))
    if (exceptions.version !== 1 || exceptions.npm.length !== 0) throw Error("security waiver needs explicit release review")
    for (const required of ["windows", "linux", "security", "frontend", "integration", "basic", "admin", "docs", "node", "compatibility"]) if (local.checks?.[required]?.passed !== true) throw Error(`local validation missing: ${required}`)
    const api = JSON.parse(readFileSync(join(root, "release/api-v1.json")))
    if (local.runtimeSHA256 !== runtimeFingerprint(root, api).sha256) throw Error("validation is for different runtime/dependencies")
    for (const item of Object.values(local.checks)) {
      if (!/^docs\/releases\/v1\.0-proposal\/validation\/[a-z-]+\.log\.gz$/.test(item.log ?? "")) throw Error("validation log path missing/invalid")
      if (hash(gunzipSync(readFileSync(join(root, item.log)))) !== item.logSHA256) throw Error("validation log checksum mismatch")
    }
  })
  check("stability and upgrade contracts", () => {
    for (const file of [freeze.stableScope, freeze.upgradeGuide, "docs/release-checklist-v1.md"]) if (!existsSync(join(root, file))) throw Error(`missing ${file}`)
  })
  let approval
  if (approvalFile) approval = JSON.parse(readFileSync(resolve(approvalFile)))
  blockers.push(...externalBlockers(freeze, approval, head))
  return { schema: 1, targetVersion: freeze.targetVersion, candidate: freeze.candidate, sourceCommit: head, checks, ready: blockers.length === 0, blockers }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = resolve(fileURLToPath(new URL("..", import.meta.url)))
  const report = assess(root, process.argv[2])
  console.log(JSON.stringify(report, null, 2))
  if (!report.ready) process.exitCode = 1
}
