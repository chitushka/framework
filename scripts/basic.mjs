import { spawn, spawnSync } from "node:child_process"
import { mkdirSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const frontend = join(root, "frontend")
const binary = join(root, ".cache", "basic", process.platform === "win32" ? "basic.exe" : "basic")
const command = process.argv[2] ?? "start"
if (!["start", "build", "test", "test-built"].includes(command)) {
  throw new Error("Usage: node scripts/basic.mjs [start|build|test|test-built]")
}

function run(executable, args, cwd = root) {
  const result = spawnSync(executable, args, { cwd, stdio: "inherit", windowsHide: true })
  if (result.error) throw result.error
  if (result.status !== 0) process.exit(result.status ?? 1)
}

function npm(args) {
  // Only fixed script arguments reach cmd.exe; no user input is interpolated.
  if (process.platform === "win32") run(process.env.ComSpec ?? "cmd.exe", ["/d", "/s", "/c", "npm.cmd", ...args], frontend)
  else run("npm", args, frontend)
}

if (command !== "test-built") {
  npm(["ci", "--no-audit", "--no-fund"])
  npm(["run", "build"])
  npm(["test"])
  mkdirSync(dirname(binary), { recursive: true })
  run("go", ["build", "-trimpath", "-o", binary, "./examples/basic"])
}
if (command === "build") process.exit(0)
if (command === "test" || command === "test-built") {
  process.env.BASIC_BINARY ??= binary
  npm(["run", "test:e2e"])
} else {
  const child = spawn(binary, [], { cwd: root, stdio: "inherit", windowsHide: true })
  for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => {
    // Windows delivers console Ctrl-C to both processes. Child.kill would force
    // termination there before Go can drain its requests.
    if (process.platform !== "win32" || signal !== "SIGINT") child.kill(signal)
  })
  child.on("error", (error) => { console.error(error.message); process.exitCode = 1 })
  child.on("exit", (code, signal) => { process.exitCode = code ?? (signal ? 1 : 0) })
}
