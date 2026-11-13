import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { requireSuccess } from "./require-success.mjs"

test("required jobs must all run and succeed", () => {
  requireSuccess({ matrix: { result: "success" }, security: { result: "success" } }, ["matrix", "security"])
  for (const state of ["failure", "cancelled", "skipped", undefined]) {
    assert.throws(() => requireSuccess({ matrix: { result: state } }, ["matrix"]))
  }
  assert.throws(() => requireSuccess({}, ["missing"]))
  assert.throws(() => requireSuccess(null, ["matrix"]))
})

test("aggregate gate covers every CI job and actions are immutable", () => {
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8")
  const jobs = [...workflow.split("\njobs:\n")[1].matchAll(/^  ([\w-]+):\s*$/gm)].map((match) => match[1]).filter((name) => name !== "quality-gate")
  const required = workflow.match(/^    needs: \[([^\]]+)\]$/m)?.[1].split(",").map((value) => value.trim())
  assert.deepEqual(required?.sort(), jobs.sort())
  for (const action of workflow.matchAll(/uses: ([^\s]+)@([^\s]+)/g)) assert.match(action[2], /^[a-f0-9]{40}$/)
  const release = readFileSync(new URL("../.github/workflows/release-check.yml", import.meta.url), "utf8")
  assert.match(release, /uses: \.\/\.github\/workflows\/ci\.yml/)
  assert.match(release, /needs: quality/)
  const security = readFileSync(new URL("./security.sh", import.meta.url), "utf8")
  assert.match(security, /govulncheck" -format=text -test/)
  assert.doesNotMatch(security, /govulncheck"[^\n]*-(json|format=(json|sarif|openvex))/)
})
