import { spawnSync } from "node:child_process"
import { mkdirSync, readFileSync, writeFileSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const severities = new Set(["info", "low", "moderate", "high", "critical"])

export function evaluateAudit(report, policy, now = new Date()) {
  if (report?.error || report?.auditReportVersion !== 2 || !report.vulnerabilities || Array.isArray(report.vulnerabilities) || !report.metadata?.vulnerabilities) throw new Error("invalid or failed npm audit report")
  if (policy?.version !== 1 || !Array.isArray(policy.npm)) throw new Error("invalid npm exception policy")
  const exceptions = new Map()
  for (const item of policy.npm) {
    const start = Date.parse(item.created), end = Date.parse(item.expires)
    if (!item.package || !/^GHSA-[\w-]+$/.test(item.advisory ?? "") || !item.owner || (item.reason?.length ?? 0) < 20 ||
        !Number.isFinite(start) || !Number.isFinite(end) || start > now.getTime() || end <= now.getTime() || end - start > 14 * 86400000) throw new Error("invalid or expired npm exception")
    const key = `${item.package}:${item.advisory}`
    if (exceptions.has(key)) throw new Error("duplicate npm exception")
    exceptions.set(key, item)
  }
  const findings = new Map()
  for (const [name, entry] of Object.entries(report.vulnerabilities)) {
    if (!severities.has(entry.severity) || !Array.isArray(entry.via)) throw new Error(`invalid audit entry: ${name}`)
    for (const advisory of entry.via) {
      if (typeof advisory === "string") {
        if (!report.vulnerabilities[advisory]) throw new Error("unresolved indirect advisory")
        continue
      }
      const id = advisory?.url?.match(/\/advisories\/(GHSA-[\w-]+)$/)?.[1]
      if (!id || !severities.has(advisory.severity)) throw new Error("invalid advisory")
      findings.set(`${name}:${id}`, advisory)
    }
  }
  const total = report.metadata.vulnerabilities.total
  const counts = [...severities].map((name) => report.metadata.vulnerabilities[name])
  if (!Number.isInteger(total) || total < 0 || counts.some((count) => !Number.isInteger(count) || count < 0) || counts.reduce((sum, count) => sum + count, 0) !== total ||
      (total > 0 && findings.size === 0) || (total === 0 && Object.keys(report.vulnerabilities).length !== 0)) throw new Error("inconsistent audit totals")
  for (const [key, advisory] of findings) {
    if (["high", "critical"].includes(advisory.severity) || !exceptions.has(key)) throw new Error(`unapproved vulnerability: ${key}`)
  }
  for (const key of exceptions.keys()) if (!findings.has(key)) throw new Error(`unused npm exception: ${key}`)
  return findings.size
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
  mkdirSync(join(root, ".cache/security"), { recursive: true })
  const args = process.platform === "win32" ? ["/d", "/s", "/c", "npm.cmd", "audit", "--json"] : ["audit", "--json"]
  const result = spawnSync(process.platform === "win32" ? (process.env.ComSpec ?? "cmd.exe") : "npm", args, { cwd: join(root, "frontend"), encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: 120000, windowsHide: true })
  writeFileSync(join(root, ".cache/security/npm-audit.json"), result.stdout ?? "")
  if (result.error || ![0, 1].includes(result.status)) throw result.error ?? new Error(`npm audit exited ${result.status}`)
  const count = evaluateAudit(JSON.parse(result.stdout), JSON.parse(readFileSync(join(root, ".github/security-exceptions.json"), "utf8")))
  console.log(`npm audit: ${count} explicitly approved advisories; no unapproved vulnerabilities`)
}
