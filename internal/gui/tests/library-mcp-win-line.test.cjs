// Run with Node's test runner and Playwright on the module path; see README.md.
// A command server's row shows its command line as the system's own shell
// reads it. On Windows that is cmd: `cmd /c C:\…\npx.cmd -y …` as written,
// double quotes only round an argument with a space, never sh's single
// quotes round a path with a \ in it (fremontdango on Discord read them as
// part of the command). The editor's command field reads the line back the
// same way: a path typed as cmd takes it keeps its \ and : (#2685), and a
// server saved unchanged keeps every argument. Elsewhere sh's quotes stay.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "C:\\Users\\fd";
const servers = [
  { name: "chrome-devtools", transport: "stdio", command: "cmd", args: ["/c", "C:\\Users\\fd\\AppData\\Roaming\\fnm\\node-versions\\v24.19.0\\installation\\npx.cmd", "-y", "chrome-devtools-mcp@latest"], agents: ["claude"] },
  { name: "spaced", transport: "stdio", command: "C:\\Program Files\\nodejs\\node.exe", args: ["say \"hi\"", ""], agents: ["claude"] },
];
const fixture = (servers) => ({
  dir: `${HOME}\\.magpie\\library`, backups: `${HOME}\\.magpie\\backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}\\.claude\\skills`, mcp: `${HOME}\\.claude.json` }],
  instructions: { agents: [], sets: [] }, foundServers: [], foundSkills: [], skills: [], projects: [], servers,
});
// #2685's line, as its reporter typed it
const TYPED = "C:\\Users\\xxx\\AppData\\Roaming\\fnm\\aliases\\default\\npx.cmd -y chrome-devtools-mcp@latest";

function server(saves) {
  const lib = fixture(structuredClone(servers));
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang: "en" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: sv } = req.postDataJSON();
      saves.push(sv);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...sv };
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
    }
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  Win32: {
    "chrome-devtools": "cmd /c C:\\Users\\fd\\AppData\\Roaming\\fnm\\node-versions\\v24.19.0\\installation\\npx.cmd -y chrome-devtools-mcp@latest",
    spaced: '"C:\\Program Files\\nodejs\\node.exe" "say ""hi""" ""',
  },
  MacIntel: {
    "chrome-devtools": "cmd /c 'C:\\Users\\fd\\AppData\\Roaming\\fnm\\node-versions\\v24.19.0\\installation\\npx.cmd' -y chrome-devtools-mcp@latest",
    spaced: "'C:\\Program Files\\nodejs\\node.exe' 'say \"hi\"' ''",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a command server's line is written as this system's shell reads it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });

    for (const platform of Object.keys(want)) {
      await t.test(platform, async () => {
        const ctx = await browser.newContext({ viewport: { width: 980, height: 500 } });
        await ctx.addInitScript((p) => {
          Object.defineProperty(Navigator.prototype, "platform", { get: () => p });
          try { localStorage.setItem("magpie.libTab", "mcp"); } catch {}
        }, platform);
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const saves = [];
        await page.route("http://magpie.test/**", server(saves));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
        const lines = await page.locator("#view-library .lib-row .sub.mono").evaluateAll((els) => els.map((e) => [e.textContent, e.title]));
        for (const [name, line] of Object.entries(want[platform])) {
          assert(lines.some(([text, tip]) => text === line && tip === line), `${name}: want ${line}, got ${JSON.stringify(lines)}`);
        }

        // the editor's field shows the same line, and saving it unchanged
        // keeps every argument as it was
        const ed = page.locator("#modal .lib-editor");
        const field = ed.locator("textarea.lib-cmd");
        const row = (name) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${name}"]`) });
        const edit = async (name, line) => {
          await row(name).locator(".name").click();
          await ed.waitFor();
          assert.equal(await field.inputValue(), want[platform][name]);
          if (line !== undefined) await field.fill(line);
          await ed.locator(":scope > .bar button.primary").click();
          await ed.waitFor({ state: "detached" });
          return saves.at(-1);
        };
        for (const name of ["spaced", "chrome-devtools"]) {
          const was = servers.find((x) => x.name === name), got = await edit(name);
          assert.deepEqual([got.command, ...(got.args || [])], [was.command, ...was.args], name);
        }
        // on Windows a path typed as it is keeps its \ and : (#2685)
        if (platform === "Win32") {
          const got = await edit("chrome-devtools", TYPED);
          assert.deepEqual([got.command, ...got.args], ["C:\\Users\\xxx\\AppData\\Roaming\\fnm\\aliases\\default\\npx.cmd", "-y", "chrome-devtools-mcp@latest"]);
        }
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
