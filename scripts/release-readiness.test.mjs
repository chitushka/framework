import assert from "node:assert/strict"
import test from "node:test"
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { externalBlockers } from "../release/readiness.mjs"
import { hash, compareRuntime } from "../release/compatibility.mjs"

function inputs() {
  const freeze = { targetVersion: "v1.0.0", repository: "chitushka/framework", requiredCheck: "Required quality gate", candidate: { revision: "b".repeat(40), version: "v0.1.0-rc.1" }, apiSHA256: "c".repeat(64), mandatoryJobs: ["test", "lint", "security", "compatibility"], requiredArtifacts: ["matrix", "security-reports"] }
  const head = "a".repeat(40)
  const proof = { schema: 1, sourceCommit: head, targetVersion: freeze.targetVersion, apiSHA256: freeze.apiSHA256, candidateRevision: freeze.candidate.revision, hostedCI: { repository: freeze.repository, headSHA: head, conclusion: "success", runURL: "https://github.com/chitushka/framework/actions/runs/123", jobs: Object.fromEntries([...freeze.mandatoryJobs, "quality-gate"].map((name) => [name, Array.from({ length: ["test", "lint"].includes(name) ? 4 : 1 }, (_, i) => ({ name: name+i, conclusion: "success" }))])), artifacts: freeze.requiredArtifacts.map((name, i) => ({ name, id: i+1, expired: false })) }, requiredChecks: { repository: freeze.repository, branch: "master", contexts: [freeze.requiredCheck], strict: true, enforceAdmins: true, bypassRestricted: true }, ownerSignoff: { login: "synthetic-fixture-owner", approvedUTC: "2026-11-18T00:00:00Z", apiReviewed: true, upgradeReviewed: true, noOpenP0Confirmed: true, securityGatesReviewed: true, checklistSHA256: hash(JSON.stringify({ targetVersion: freeze.targetVersion, apiSHA256: freeze.apiSHA256, candidate: freeze.candidate, sourceCommit: head })) }, publicationPermission: { sourceCommit: head, targetVersion: freeze.targetVersion, explicitlyAuthorized: true } }
  // Synthetic in-memory receipt; this is never written as an owner signature.
  proof.ownerSignoff.login = freeze.repository.split("/")[0]
  return { freeze, head, proof }
}
const blockers = ({ freeze, head, proof }) => externalBlockers(freeze, proof, head, new Date("2026-11-18T12:00:00Z"))

test("release requires complete hosted matrix, artifact identity and explicit owner approval", () => {
  assert.deepEqual(blockers(inputs()), [])
  const { freeze, head } = inputs()
  assert.ok(externalBlockers(freeze, null, head).length > 0)
  for (const mutate of [
    (i) => { i.proof.sourceCommit = "d".repeat(40) },
    (i) => { i.proof.apiSHA256 = "d".repeat(64) },
    (i) => { i.proof.candidateRevision = "d".repeat(40) },
    (i) => { i.proof.hostedCI.headSHA = "d".repeat(40) },
    (i) => { i.proof.hostedCI.conclusion = "failure" },
    (i) => { i.proof.hostedCI.runURL = "https://example.test/actions/runs/123" },
    (i) => { i.proof.hostedCI.jobs.test.pop() },
    (i) => { i.proof.hostedCI.jobs.lint[0].conclusion = "skipped" },
    (i) => { delete i.proof.hostedCI.jobs["quality-gate"] },
    (i) => { i.proof.hostedCI.artifacts[0].expired = true },
    (i) => { i.proof.requiredChecks.contexts = [] },
    (i) => { i.proof.requiredChecks.enforceAdmins = false },
    (i) => { i.proof.requiredChecks.bypassRestricted = false },
    (i) => { i.proof.ownerSignoff = null },
    (i) => { i.proof.ownerSignoff.login = "different-account" },
    (i) => { i.proof.ownerSignoff.checklistSHA256 = "d".repeat(64) },
    (i) => { i.proof.ownerSignoff.approvedUTC = "2099-01-01T00:00:00Z" },
    (i) => { i.proof.publicationPermission.explicitlyAuthorized = false },
  ]) { const input = inputs(); mutate(input); assert.ok(blockers(input).length > 0, mutate.toString()) }
})

test("runtime identity tolerates CRLF but rejects implementations, dependencies and added source", () => {
  const directory = mkdtempSync(join(tmpdir(), "chitu-runtime-identity-"))
  try {
    const current = join(directory, "current"), reference = join(directory, "reference")
    for (const root of [current, reference]) {
      for (const path of ["web", "internal/httpwriter", "adapters"]) mkdirSync(join(root, path), { recursive: true })
      writeFileSync(join(root, "web/response.go"), "package web\nconst Value=1\n")
      for (const file of ["go.mod", "go.sum", "adapters/go.mod", "adapters/go.sum"]) writeFileSync(join(root, file), "fixture\n")
    }
    const api = { packages: { "github.com/chitushka/framework/web": {} } }
    writeFileSync(join(current, "web/response.go"), "package web\r\nconst Value=1\r\n")
    assert.equal(compareRuntime(current, reference, api).files, 5)
    writeFileSync(join(current, "web/response.go"), "package web\nconst Value=2\n")
    assert.throws(() => compareRuntime(current, reference, api), /drift/)
    writeFileSync(join(current, "web/response.go"), "package web\nconst Value=1\n")
    writeFileSync(join(current, "adapters/go.mod"), "changed dependency\n")
    assert.throws(() => compareRuntime(current, reference, api), /drift/)
    writeFileSync(join(current, "adapters/go.mod"), "fixture\n")
    writeFileSync(join(reference, "adapters/go.mod"), "require github.com/chitushka/framework v0.1.0-rc.1\n")
    writeFileSync(join(current, "adapters/go.mod"), "require github.com/chitushka/framework v1.0.0\n")
    assert.equal(compareRuntime(current, reference, api).files, 5)
    writeFileSync(join(current, "adapters/go.mod"), "require github.com/chitushka/framework v1.2.0\n")
    assert.throws(() => compareRuntime(current, reference, api), /unreviewed/)
    writeFileSync(join(current, "adapters/go.mod"), "require github.com/chitushka/framework v1.0.0\n")
    writeFileSync(join(current, "web/addition.go"), "package web\n")
    assert.throws(() => compareRuntime(current, reference, api), /drift/)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
