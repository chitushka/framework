import { spawn, spawnSync } from "node:child_process"
import { mkdirSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const frontend = join(root, "frontend")
const binary = join(root, ".cache/admin", process.platform === "win32" ? "admin.exe" : "admin")
const command = process.argv[2] ?? "start"
if (!["start", "build", "migrate", "seed", "test-built"].includes(command)) throw new Error("Usage: node scripts/admin.mjs [start|build|migrate|seed|test-built]")
function run(executable, args, cwd = root) {
  const result = spawnSync(executable, args, { cwd, stdio: "inherit", windowsHide: true })
  if (result.error) throw result.error
  if (result.status !== 0) process.exit(result.status ?? 1)
}
function npm(args) {
  if (process.platform === "win32") run(process.env.ComSpec ?? "cmd.exe", ["/d", "/s", "/c", "npm.cmd", ...args], frontend)
  else run("npm", args, frontend)
}
if (["start", "build"].includes(command)) {
  npm(["ci", "--no-audit", "--no-fund"])
  npm(["run", "build"])
  npm(["test"])
  mkdirSync(dirname(binary), { recursive: true })
  run("go", ["-C", "examples/advanced-admin", "build", "-trimpath", "-o", binary, "."])
}
if (command === "build") process.exit(0)
if (command === "test-built") {
  process.env.ADMIN_BINARY ??= binary
  npm(["run", "test:admin"])
} else if (command === "migrate" || command === "seed") {
  run(binary, [`-${command}`])
} else {
  const child = spawn(binary, [], { cwd: root, stdio: "inherit", windowsHide: true })
  for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => { if (process.platform !== "win32" || signal !== "SIGINT") child.kill(signal) })
  child.on("error", (error) => { console.error(error.message); process.exitCode = 1 })
  child.on("exit", (code, signal) => { process.exitCode = code ?? (signal ? 1 : 0) })
}
