import assert from "node:assert/strict"
import test from "node:test"
import { evaluateAudit, collectAdvisories } from "./npm-audit.mjs"

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

function withdrawal() {
  return { "GHSA-aaaa-bbbb-cccc": { checkedUTC: now.toISOString(), advisory: { ghsa_id: "GHSA-aaaa-bbbb-cccc", type: "reviewed", url: "https://api.github.com/advisories/GHSA-aaaa-bbbb-cccc", html_url: "https://github.com/advisories/GHSA-aaaa-bbbb-cccc", withdrawn_at: "2026-11-13T11:00:00Z", published_at: "2026-11-12T00:00:00Z", updated_at: "2026-11-13T11:00:00Z", vulnerabilities: [{ package: { ecosystem: "npm", name: "example" } }] } } }
}
test("only a fresh canonical withdrawal removes an obsolete finding; active high/critical still fail", () => {
  const report = vulnerable("high")
  assert.equal(evaluateAudit(report, policy(), now, withdrawal()), 0)
  assert.throws(() => evaluateAudit(report, { version: 1, npm: [waiver()] }, now, withdrawal()), /unused/)
  assert.equal(report.metadata.vulnerabilities.high, 1, "raw npm report must remain intact")
  for (const mutate of [
    (record) => { record.advisory.withdrawn_at = null },
    (record) => { record.checkedUTC = "2026-11-13T11:54:59Z" },
    (record) => { record.checkedUTC = "2026-11-13T12:01:00Z" },
    (record) => { record.advisory.withdrawn_at = "2026-11-14T00:00:00Z" },
    (record) => { record.advisory.ghsa_id = "GHSA-dddd-eeee-ffff" },
    (record) => { record.advisory.url = "https://evil.test/advisory" },
    (record) => { record.advisory.vulnerabilities[0].package.name = "different-package" },
    (record) => { delete record.advisory.withdrawn_at },
  ]) { const verified = withdrawal(); mutate(verified["GHSA-aaaa-bbbb-cccc"]); assert.throws(() => evaluateAudit(report, policy(), now, verified)) }
  for (const severity of ["high", "critical"]) assert.throws(() => evaluateAudit(vulnerable(severity), policy(), now))
})

test("advisory verification is bounded, uses a fixed origin, and fails on network/status/JSON failures", async () => {
  let called
  const records = await collectAdvisories(vulnerable("high"), async (url, options) => {
    called = { url, options }
    return { ok: true, text: async () => JSON.stringify(withdrawal()["GHSA-aaaa-bbbb-cccc"].advisory) }
  }, now)
  assert.equal(evaluateAudit(vulnerable("high"), policy(), now, records), 0)
  assert.equal(called.url, "https://api.github.com/advisories/GHSA-aaaa-bbbb-cccc")
  assert.equal(called.options.redirect, "error")
  assert.ok(called.options.signal)
  for (const fetcher of [async () => { throw Error("network") }, async () => ({ ok: false, status: 403 }), async () => ({ ok: true, text: async () => "invalid JSON" })]) await assert.rejects(() => collectAdvisories(vulnerable(), fetcher, now))
  assert.deepEqual(await collectAdvisories(clean(), async () => { throw Error("unexpected network") }, now), {})
})
