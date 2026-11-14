import { readFileSync, writeFileSync, mkdirSync, copyFileSync } from "node:fs"
import { join } from "node:path"

// Preserve just the selected manifest closure; fail if Vite emits unsafe paths.
export function selectAssets(source, output, entry) {
  const manifest = JSON.parse(readFileSync(join(source, ".vite/manifest.json"), "utf8"))
  const selected = {}, files = new Set()
  function visit(key) {
    if (selected[key]) return
    const item = manifest[key]
    if (!item) throw Error(`Missing Vite entry ${key}`)
    selected[key] = item
    for (const path of [item.file, ...(item.css ?? []), ...(item.assets ?? [])]) {
      if (!/^assets\/[a-zA-Z0-9_.-]+$/.test(path)) throw Error(`Unsafe asset ${path}`)
      files.add(path)
    }
    for (const key of [...(item.imports ?? []), ...(item.dynamicImports ?? [])]) visit(key)
  }
  visit(entry)
  mkdirSync(join(output, ".vite"), { recursive: true })
  mkdirSync(join(output, "assets"), { recursive: true })
  for (const file of [...files].sort()) copyFileSync(join(source, file), join(output, file))
  writeFileSync(join(output, ".vite/manifest.json"), JSON.stringify(selected, null, 2) + "\n")
}
if (process.argv[2]) selectAssets(...process.argv.slice(2))
