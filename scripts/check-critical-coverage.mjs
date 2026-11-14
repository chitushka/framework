import { readFileSync } from "node:fs"
import { pathToFileURL } from "node:url"

export const thresholds = Object.freeze({ auth: 85, session: 85, security: 90 })
const prefix = "github.com/chitushka/framework/"

// Use statement counts, before rounding, so 84.99% cannot pass an 85% gate.
function requireCoverage(totals, label, threshold) {
  const value = String(threshold)
  if (value.length > 1024 || !/^\d+(\.\d+)?$/.test(value)) throw new Error("invalid coverage threshold")
  const [whole, fraction = ""] = value.split(".")
  const scale = 10n ** BigInt(fraction.length)
  const required = BigInt(whole + fraction)
  if (required > 100n * scale) throw new Error("invalid coverage threshold")
  if (!totals || totals.total === 0n) throw new Error(`missing coverage statements for ${label}`)
  const actual = Number(totals.covered * 10000n / totals.total) / 100
  if (totals.covered * 100n * scale < totals.total * required) {
    throw new Error(`${label} coverage: ${actual.toFixed(2)}% (required: ${value}%)`)
  }
  return `${label} coverage: ${actual.toFixed(2)}% (required: ${value}%)`
}

export function checkCriticalCoverage(profile, coreThreshold) {
  const lines = profile.trimEnd().split(/\r?\n/)
  if (!/^mode: (set|count|atomic)$/.test(lines.shift() ?? "")) throw new Error("invalid coverage mode")
  const packages = new Map()
  const seen = new Set()
  for (const line of lines) {
    const match = /^(.*\.go):([1-9]\d*)\.([1-9]\d*),([1-9]\d*)\.([1-9]\d*) (\d+) (\d+)$/.exec(line)
    if (!match || !match[1].startsWith(prefix)) throw new Error(`invalid coverage block: ${line}`)
    const [, file, startLine, startColumn, endLine, endColumn, statements, count] = match
    if (BigInt(endLine) < BigInt(startLine) || (BigInt(endLine) === BigInt(startLine) &&
      (BigInt(endColumn) < BigInt(startColumn) || (endColumn === startColumn && BigInt(statements) !== 0n)))) {
      throw new Error("invalid coverage range")
    }
    const key = `${file}:${startLine}.${startColumn},${endLine}.${endColumn}`
    if (seen.has(key)) throw new Error(`duplicate coverage block: ${key}`)
    seen.add(key)
    const pkg = file.slice(prefix.length, file.lastIndexOf("/"))
    const totals = packages.get(pkg) ?? { total: 0n, covered: 0n }
    totals.total += BigInt(statements)
    if (BigInt(count) > 0n) totals.covered += BigInt(statements)
    packages.set(pkg, totals)
  }
  const results = Object.entries(thresholds).map(([pkg, threshold]) => requireCoverage(packages.get(pkg), pkg, threshold))
  if (coreThreshold !== undefined) {
    const totals = [...packages.values()].reduce((sum, value) => ({ total: sum.total + value.total, covered: sum.covered + value.covered }), { total: 0n, covered: 0n })
    results.push(requireCoverage(totals, "core profile", coreThreshold))
  }
  return results
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length < 3 || process.argv.length > 4) throw new Error("usage: node scripts/check-critical-coverage.mjs profile [core-threshold]")
    for (const result of checkCriticalCoverage(readFileSync(process.argv[2], "utf8"), process.argv[3])) console.log(result)
  } catch (error) {
    console.error(`coverage: ${error.message}`)
    process.exitCode = 1
  }
}
