import test from "node:test"
import assert from "node:assert/strict"
import { readFile, stat } from "node:fs/promises"
import { resolve, sep } from "node:path"

test("Vite builds both the Vue scaffold and Basic with resolvable hashed assets", async () => {
  const root = resolve("../public")
  const manifest = JSON.parse(await readFile(resolve(root, ".vite/manifest.json"), "utf8"))
  for (const key of ["src/main.js", "src/basic.js"]) {
    const entry = manifest[key]
    assert.ok(entry, `missing entry: ${key}`)
    assert.ok(entry.css?.length, `missing stylesheet: ${key}`)
    for (const file of [entry.file, ...entry.css]) {
      const path = resolve(root, file)
      assert.ok(path.startsWith(root + sep))
      assert.match(file, /^assets\/.+-[\w-]+\.(js|css)$/)
      assert.ok((await stat(path)).size > 0)
    }
  }
  const css = await readFile(resolve(root, manifest["src/basic.js"].css[0]), "utf8")
  assert.ok(css.includes(".login-shell") && css.includes(".dashboard-shell"))
})
