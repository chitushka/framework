import { spawnSync } from "node:child_process"
import { mkdirSync, readFileSync, writeFileSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const severities = new Set(["info", "low", "moderate", "high", "critical"])
const advisoryID = (url) => url?.match(/^https:\/\/github\.com\/advisories\/(GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})$/)?.[1]

export function verifiedWithdrawal(record, id, name, now) {
  const data = record?.advisory, checked = Date.parse(record?.checkedUTC ?? "")
  if (!data || data.ghsa_id !== id || data.type !== "reviewed" ||
      data.url !== `https://api.github.com/advisories/${id}` || data.html_url !== `https://github.com/advisories/${id}` ||
      !Number.isFinite(checked) || checked > now.getTime() || now.getTime()-checked > 5*60_000 ||
      !data.vulnerabilities?.some((v) => v.package?.ecosystem === "npm" && v.package.name === name) ||
      !Object.hasOwn(data, "withdrawn_at")) throw Error("invalid/stale GitHub advisory verification")
  const published = Date.parse(data.published_at), updated = Date.parse(data.updated_at)
  if (![published, updated].every(Number.isFinite) || updated < published || updated > now.getTime()) throw Error("invalid GitHub advisory timestamp")
  if (data.withdrawn_at === null) return false
  const withdrawn = Date.parse(data.withdrawn_at)
  if (!Number.isFinite(withdrawn) || withdrawn > now.getTime() || withdrawn < published || updated < withdrawn) throw Error("invalid GitHub withdrawal timestamp")
  return true
}

export async function collectAdvisories(report, fetcher = fetch, now = new Date()) {
  const ids = [...new Set(Object.values(report.vulnerabilities ?? {}).flatMap((entry) => Array.isArray(entry.via) ? entry.via.filter((via) => typeof via === "object").map((via) => advisoryID(via.url)).filter(Boolean) : []))]
  if (ids.length > 32) throw Error("advisory verification exceeds bounded request limit")
  const records = await Promise.all(ids.map(async (id) => {
    const url = `https://api.github.com/advisories/${id}`
    const headers = { Accept: "application/vnd.github+json", "User-Agent": "chitu-security-gate", "X-GitHub-Api-Version": "2026-03-10" }
    const token = process.env.GH_TOKEN ?? process.env.GITHUB_TOKEN
    if (token) headers.Authorization = `Bearer ${token}`
    const response = await fetcher(url, { headers, redirect: "error", signal: AbortSignal.timeout(10_000) })
    if (!response.ok) throw Error(`GitHub advisory verification HTTP ${response.status}`)
    const text = await response.text()
    if (Buffer.byteLength(text) > 512*1024) throw Error("GitHub advisory response exceeds limit")
    const data = JSON.parse(text)
    // Retain factual provenance only; no credentials or copied descriptions.
    const advisory = Object.fromEntries(["ghsa_id", "url", "html_url", "type", "withdrawn_at", "published_at", "updated_at"].map((key) => [key, data[key]]))
    advisory.vulnerabilities = data.vulnerabilities?.map((v) => ({ package: v.package }))
    return [id, { checkedUTC: now.toISOString(), advisory }]
  }))
  return Object.fromEntries(records)
}

export function evaluateAudit(report, policy, now = new Date(), verifiedAdvisories = {}) {
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
      const id = advisoryID(advisory?.url)
      if (!id || !severities.has(advisory.severity)) throw new Error("invalid advisory")
      findings.set(`${name}:${id}`, advisory)
    }
  }
  const total = report.metadata.vulnerabilities.total
  const counts = [...severities].map((name) => report.metadata.vulnerabilities[name])
  if (!Number.isInteger(total) || total < 0 || counts.some((count) => !Number.isInteger(count) || count < 0) || counts.reduce((sum, count) => sum + count, 0) !== total ||
      (total > 0 && findings.size === 0) || (total === 0 && Object.keys(report.vulnerabilities).length !== 0)) throw new Error("inconsistent audit totals")
  let active = 0
  const activeKeys = new Set()
  for (const [key, advisory] of findings) {
    const separator = key.lastIndexOf(":GHSA-")
    const name = key.slice(0, separator), id = key.slice(separator+1)
    if (verifiedAdvisories[id] && verifiedWithdrawal(verifiedAdvisories[id], id, name, now)) continue
    active++
    activeKeys.add(key)
    if (["high", "critical"].includes(advisory.severity) || !exceptions.has(key)) throw new Error(`unapproved vulnerability: ${key}`)
  }
  for (const key of exceptions.keys()) if (!activeKeys.has(key)) throw new Error(`unused npm exception: ${key}`)
  return active
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
  mkdirSync(join(root, ".cache/security"), { recursive: true })
  const args = process.platform === "win32" ? ["/d", "/s", "/c", "npm.cmd", "audit", "--json"] : ["audit", "--json"]
  const result = spawnSync(process.platform === "win32" ? (process.env.ComSpec ?? "cmd.exe") : "npm", args, { cwd: join(root, "frontend"), encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: 120000, windowsHide: true })
  writeFileSync(join(root, ".cache/security/npm-audit.json"), result.stdout ?? "")
  if (result.error || ![0, 1].includes(result.status)) throw result.error ?? new Error(`npm audit exited ${result.status}`)
  const report = JSON.parse(result.stdout), now = new Date()
  const verified = await collectAdvisories(report, fetch, now)
  writeFileSync(join(root, ".cache/security/npm-advisories.json"), JSON.stringify(verified, null, 2)+"\n")
  const count = evaluateAudit(report, JSON.parse(readFileSync(join(root, ".github/security-exceptions.json"), "utf8")), now, verified)
  const withdrawn = Object.values(verified).filter((record) => record.advisory.withdrawn_at !== null).length
  console.log(`npm audit: ${count} approved active advisories; ${withdrawn} freshly verified withdrawn; no unapproved active vulnerabilities`)
}
