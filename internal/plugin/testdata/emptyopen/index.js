// A plugin that opens an empty target on every route a program can be
// started by (Bun.spawn in both forms, Bun.spawnSync, node:child_process
// spawn, exec and execFile and their Sync forms), and a real one on each,
// and once from a Bun script it starts, as a page plugin's scan of the
// other plugins did (unbrokefix on 𝕏). The programs are fakes: copies of
// Bun named explorer, open and xdg-open, which run the script they are
// given as their "target" and write its tag down. The link it hands on
// holds what each case did, and the argv shapes emptyOpen got wrong.
import cp from "node:child_process"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"

const win = process.platform === "win32"
const exe = win ? ".exe" : ""
const opens = () => globalThis[Symbol.for("magpie.opens")]

function fakes() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "emptyopen-"))
  for (const n of ["explorer", "open", "xdg-open", "cmd"]) {
    const to = path.join(dir, n + exe)
    try {
      if (win) fs.linkSync(process.execPath, to)
      else fs.symlinkSync(process.execPath, to)
    } catch {
      fs.copyFileSync(process.execPath, to)
      fs.chmodSync(to, 0o755)
    }
  }
  const log = path.join(dir, "ran.log")
  const script = path.join(dir, "target.js")
  fs.writeFileSync(script, `require("node:fs").appendFileSync(${JSON.stringify(log)}, process.argv[process.argv.length - 1] + "\\n")`)
  return { dir, log, script, fake: (n) => path.join(dir, n + exe) }
}

const ended = (child) => new Promise((resolve) => (child.exited ? child.exited.then(resolve) : child.on("close", resolve)))
const q = (s) => `"${s}"`

async function cases(f) {
  const { fake, script } = f
  // the empty target each route is given, and the real one
  const empty = [fake("cmd"), "/d", "/s", "/c", '"start "" """']
  const shellEmpty = win ? `${q(fake("explorer"))} ""` : 'open ""'
  const real = (tag) => [fake("explorer"), script, tag]
  const shellReal = (tag) => (win ? `${q(fake("explorer"))} ${q(script)} ${tag}` : `open ${q(script)} ${tag}`)
  const env = { ...process.env, PATH: f.dir + path.delimiter + process.env.PATH }
  const o = { stdio: "ignore", env }
  return [
    ["Bun.spawn", async (real) => ended(Bun.spawn(real ?? empty, { stdout: "ignore", stderr: "ignore", env }))],
    ["Bun.spawn({cmd})", async (real) => ended(Bun.spawn({ cmd: real ?? [fake("open"), ""], stdout: "ignore", stderr: "ignore", env }))],
    ["Bun.spawnSync", async (real) => Bun.spawnSync(real ?? [fake("xdg-open"), ""], { env })],
    ["spawn", async (real) => ended(cp.spawn((real ?? empty)[0], (real ?? empty).slice(1), { ...o, windowsVerbatimArguments: true }))],
    ["spawnSync", async (real) => cp.spawnSync((real ?? [fake("explorer")])[0], (real ?? [fake("explorer")]).slice(1), o)],
    ["execFile", async (real) => new Promise((r) => cp.execFile((real ?? [fake("open"), ""])[0], (real ?? [fake("open"), ""]).slice(1), o, () => r()))],
    ["execFileSync", async (real) => { try { cp.execFileSync((real ?? empty)[0], (real ?? empty).slice(1), { ...o, windowsVerbatimArguments: true }) } catch {} }],
    ["exec", async (real) => new Promise((r) => cp.exec(real ? shellReal(real[2]) : shellEmpty, o, () => r()))],
    ["execSync", async (real) => { try { cp.execSync(real ? shellReal(real[2]) : shellEmpty, o) } catch {} }],
  ].map(([name, run]) => [name, () => run(), () => run(real(name.replace(/\W/g, "_")))])
}

// shapes are argv emptyOpen is asked about, and whether each opens nothing
const shapes = [
  [["cmd.exe", "/d", "/s", "/c", 'start "" ""'], true],
  [["C:\\Windows\\system32\\cmd.exe", "/d", "/s", "/c", '"start "" """'], true],
  [["cmd", "/c", "start", '""', '""'], true],
  [["cmd", "/c", "start", '""'], true],
  [["cmd", "/c", "start"], true],
  [["cmd", "/s", "/c", "start", '""', "/b", '""'], true],
  [["cmd", "/c", 'start "" "C:\\Users\\me\\file.txt"'], false],
  [["cmd", "/s", "/c", "start", '""', "/b", "https://example.com/?a=1^&b=2"], false],
  [["cmd", "/d", "/s", "/c", '"start "" "https://example.com""'], false],
  [["cmd", "/c", "start", "notepad"], false],
  [["cmd", "/c", 'start "" "" & echo hi'], false],
  [["cmd", "/c", "dir"], false],
  [["cmd", "/c", "explorer"], true],
  [["cmd", "/c", 'explorer ""'], true],
  [["cmd", "/c", "explorer C:\\Users"], false],
  [["explorer.exe"], true],
  [["C:\\Windows\\explorer.exe", ""], true],
  [["explorer.exe", "/e,"], true],
  [["explorer.exe", "C:\\Users"], false],
  [["explorer.exe", "/select,C:\\a.txt"], false],
  [["open", ""], true],
  [["/usr/bin/open", "-R", ""], true],
  [["open", "https://example.com"], false],
  [["open", "-a", "Finder"], false],
  [["xdg-open", ""], true],
  [["xdg-open", "/home/me"], false],
  [["/bin/sh", "-c", 'open ""'], true],
  [["/bin/sh", "-c", "xdg-open ''"], true],
  [["/bin/sh", "-c", "open 'https://example.com'"], false],
  [["/bin/sh", "-c", 'open "" && echo hi'], false],
  [["bun", "-e", ""], false],
  [["git", "status"], false],
]

export const EmptyOpenPlugin = async () => ({
  auth: {
    provider: "emptyopen",
    methods: [
      {
        type: "oauth",
        label: "Open",
        authorize: async () => {
          const f = fakes()
          const out = { loaded: !!opens() }
          for (const [name, empty, real] of await cases(f)) {
            const before = opens()?.refused() ?? 0
            await empty()
            const mid = opens()?.refused() ?? 0
            await real()
            out[name] = { refused: mid - before, realRefused: (opens()?.refused() ?? 0) - mid }
          }
          out.ran = fs.existsSync(f.log) ? fs.readFileSync(f.log, "utf8").split(/\r?\n/).filter(Boolean) : []
          // and from a Bun script this plugin starts with no folder: it
          // says how many it refused, and whether it opened its real one
          const child = Bun.spawnSync(
            [process.execPath, "-e", `
              const o = globalThis[Symbol.for("magpie.opens")]
              const cp = require("node:child_process")
              cp.spawnSync(${JSON.stringify(f.fake("cmd"))}, ["/d", "/s", "/c", '"start "" """'], { stdio: "ignore", windowsVerbatimArguments: true })
              cp.spawnSync(${JSON.stringify(f.fake("explorer"))}, [${JSON.stringify(f.script)}, "child"], { stdio: "ignore" })
              process.stdout.write(String(o ? o.refused() : -1))
            `],
            { stdout: "pipe", stderr: "ignore" },
          )
          out.child = Number(child.stdout.toString())
          out.ran = fs.existsSync(f.log) ? fs.readFileSync(f.log, "utf8").split(/\r?\n/).filter(Boolean) : []
          out.wrongShapes = opens() ? shapes.filter(([argv, want]) => !!opens().emptyOpen(argv) !== want).map(([argv]) => argv) : []
          fs.rmSync(f.dir, { recursive: true, force: true })
          return {
            url: `https://emptyopen.invalid/?${new URLSearchParams({ out: JSON.stringify(out) })}`,
            instructions: "",
            method: "code",
            callback: async () => ({ type: "failed" }),
          }
        },
      },
    ],
  },
})
