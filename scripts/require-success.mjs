// Aggregate GitHub needs results without accepting skipped/cancelled dependencies.
export function requireSuccess(results, required) {
  if (!results || !Array.isArray(required) || required.length === 0) throw new Error("missing gate inputs")
  for (const name of required) {
    if (results[name]?.result !== "success") throw new Error(`required job failed or did not run: ${name}`)
  }
}

if (process.argv[1]?.endsWith("require-success.mjs")) {
  requireSuccess(JSON.parse(process.env.CI_NEEDS ?? "null"), process.argv.slice(2))
  console.log("All required quality jobs succeeded")
}
