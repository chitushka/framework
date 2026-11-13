import assert from "node:assert/strict"
import test from "node:test"
import { evaluateAudit } from "./npm-audit.mjs"

const now = new Date("2026-11-13T12:00:00Z")
const clean = () => ({ auditReportVersion: 2, vulnerabilities: {}, metadata: { vulnerabilities: { info: 0, low: 0, moderate: 0, high: 0, critical: 0, total: 0 } } })
const policy = () => ({ version: 1, npm: [] })
const vulnerable = (severity = "moderate") => ({ ...clean(), vulnerabilities: { example: { severity, via: [{ url: "https://github.com/advisories/GHSA-aaaa-bbbb-cccc", severity }] } }, metadata: { vulnerabilities: { ...clean().metadata.vulnerabilities, [severity]: 1, total: 1 } } })
const waiver = () => ({ package: "example", advisory: "GHSA-aaaa-bbbb-cccc", owner: "maintainer", reason: "Temporary tooling mitigation with a tracked fix", created: "2026-11-12T00:00:00Z", expires: "2026-11-20T00:00:00Z" })

test("audit fails closed and exceptions expire", () => {
  assert.equal(evaluateAudit(clean(), policy(), now), 0)
  assert.throws(() => evaluateAudit(vulnerable(), policy(), now))
  assert.equal(evaluateAudit(vulnerable(), { version: 1, npm: [waiver()] }, now), 1)
  for (const severity of ["high", "critical"]) assert.throws(() => evaluateAudit(vulnerable(severity), { version: 1, npm: [waiver()] }, now))
  assert.throws(() => evaluateAudit(vulnerable(), { version: 1, npm: [{ ...waiver(), expires: "2026-11-13T00:00:00Z" }] }, now))
  assert.throws(() => evaluateAudit(vulnerable(), { version: 1, npm: [{ ...waiver(), expires: "2027-01-01" }] }, now))
  assert.throws(() => evaluateAudit(clean(), { version: 1, npm: [waiver()] }, now))
  for (const report of [{}, { ...clean(), error: {} }, { ...clean(), metadata: { vulnerabilities: { total: 1 } } }]) assert.throws(() => evaluateAudit(report, policy(), now))
})
