import assert from "node:assert/strict"
import test from "node:test"
import { checkCriticalCoverage } from "./check-critical-coverage.mjs"

const block = (pkg, covered = 90, missed = 10) =>
  `github.com/chitushka/framework/${pkg}/file.go:1.1,2.1 ${covered} 1\n` +
  `github.com/chitushka/framework/${pkg}/file.go:3.1,4.1 ${missed} 0\n`
const profile = (...blocks) => `mode: atomic\n${blocks.join("")}`
const good = profile(block("auth", 85, 15), block("session", 85, 15), block("security"))

test("critical package thresholds are independent and use unrounded counts", () => {
  assert.equal(checkCriticalCoverage(good).length, 3)
  assert.equal(checkCriticalCoverage(good.replaceAll("\n", "\r\n")).length, 3)
  assert.equal(checkCriticalCoverage(good + "github.com/chitushka/framework/app/app.go:5.1,5.1 0 1\n").length, 3)
  for (const pkg of ["auth", "session", "security"]) {
    const others = ["auth", "session", "security"].filter((name) => name !== pkg).map((name) => block(name, 100, 0))
    assert.throws(() => checkCriticalCoverage(profile(...others)), /missing coverage/)
    assert.throws(() => checkCriticalCoverage(profile(...others, block(pkg, 1, 99))), /coverage:/)
    assert.throws(() => checkCriticalCoverage(profile(...others, block(pkg, 0, 0))), /missing coverage/)
  }
  assert.throws(() => checkCriticalCoverage(profile(block("auth", 84999, 15001), block("session"), block("security"))), /84.99%/)
})

test("empty, corrupt, foreign and duplicated profiles fail closed", () => {
  for (const bad of ["", "mode: atomic\n", "mode: invalid\n", good + "garbage\n", good + block("auth"),
    good.replace("file.go:1.1,2.1", "file.go:2.1,1.1"), good.replace("file.go:1.1,2.1", "file.go:1.1,1.1"),
    good.replace(" 85 1", " -85 1"), good.replace(" 85 1", " 85 NaN"), good.replace("framework/auth/", "other/auth/")]) {
    assert.throws(() => checkCriticalCoverage(bad))
  }
})

test("overall gate rejects rounded-up totals and compares decimal thresholds exactly", () => {
  assert.equal(checkCriticalCoverage(good, "80").length, 4)
  assert.equal(checkCriticalCoverage(good, "86.66666666666666666666").length, 4)
  assert.throws(() => checkCriticalCoverage(good, "86.66666666666666666667"), /core profile coverage/)
  // 8000/10001 rounds to 80.0% in go tool cover, but is below the threshold.
  assert.throws(() => checkCriticalCoverage(good + block("core", 7740, 1961), "80"), /79.99%/)
  for (const threshold of ["", "-1", "NaN", "Inf", "101", ".5", "80junk", "1.2.3"]) {
    assert.throws(() => checkCriticalCoverage(good, threshold), /invalid coverage threshold/)
  }
})
