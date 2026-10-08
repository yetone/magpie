// Run with Node's test runner and Playwright on the module path; see README.md.
// #1250: a remote MCP server's headers (or a command's environment) can name
// a variable as ${NAME}. The library keeps it so, and each agent is given
// it in its own syntax. An agent that can't read a variable from its
// settings (here Antigravity, noEnvRefs) is never given such a server: its
// chip is greyed on the row and in the editor with the reason, and a value
// typed back to plain text lights it again. The editor says how to write
// one under Headers and Environment. A check magpie can't make because the
// variable isn't set where magpie runs says which one, not "couldn't check".
// In en, zh, ja and de at 440px: no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const lib = (servers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex", { noSSE: true }), agent("agy", "Antigravity", { noEnvRefs: true })],
  instructions: { agents: [] }, servers, skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
});

let I18N;
async function loadI18N() {
  const src = await fs.readFile(path.join(assets, "i18n.js"), "utf8");
  I18N = new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();
}
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, servers) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(servers));
    // gh's token isn't set where magpie runs; fs has none to need
    if (url.pathname === "/api/library/mcp/check") return json({ servers: Object.fromEntries(req.postDataJSON().names.map((n) => [n, n === "gh" ? { state: "error", why: "novar", detail: "GH_TOKEN" } : { state: "ok", tools: 2 }])) });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#modal .lib-editor, #modal .lib-editor .hint, #modal .lib-agents")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server naming ${NAME} in its headers or environment", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    await loadI18N();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const servers = [
          { name: "gh", transport: "http", url: "https://api.example.com/mcp", headers: { Authorization: "Bearer ${GH_TOKEN}", "X-Team": "core" }, agents: ["claude", "codex"] },
          { name: "fs", transport: "stdio", command: "fs-mcp", args: [], env: { ROOT: "/tmp" }, agents: ["claude", "agy"] },
        ];
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 440, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, servers));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-server").first().waitFor();
        const row = (name) => v.locator(".lib-server").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        const why = w("{agent} can't read {ref} from its settings — given this server, the token would be written there as plain text", { agent: "Antigravity", ref: "${NAME}" });

        // the row: Antigravity greyed with the reason; fs's plain env isn't
        const agy = row("gh").locator('.lib-ag[data-agent="agy"]');
        assert.equal(await agy.getAttribute("aria-disabled"), "true");
        assert.equal(await agy.getAttribute("title"), why);
        assert.equal(await row("fs").locator('.lib-ag[data-agent="agy"]').getAttribute("aria-disabled"), null, "a server with no ${NAME} was held from Antigravity");

        // the check: which variable magpie's own environment hasn't
        const status = v.locator('.lib-health[data-server="gh"]');
        await page.waitForFunction(() => document.querySelector('#view-library .lib-health[data-server="gh"]')?.classList.contains("err"));
        assert.equal((await status.textContent()).trim(), w("{names} not set", { names: "GH_TOKEN" }));
        assert.equal((await status.getAttribute("title")).split("\n")[0], w("magpie can't check it: {names} isn't set where magpie runs. An agent started where it is set still gets it", { names: "GH_TOKEN" }));

        // the editor: the hint under Headers, the chip greyed with the reason
        await row("gh").locator(".sub").click();
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        const hint = w("A value can name a variable, as {ref}: each agent is given it in its own way, so the token stays out of its settings", { ref: "${NAME}" });
        await sheet.getByText(hint, { exact: true }).waitFor();
        const chip = sheet.locator('.lib-ag[data-agent="agy"]');
        assert.equal(await chip.getAttribute("aria-disabled"), "true");
        assert.equal(await chip.getAttribute("title"), why);
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor");

        // the token typed in plain: Antigravity can have it again
        const value = sheet.locator(".lib-pair").first().locator("input").nth(1);
        assert.equal(await value.inputValue(), "Bearer ${GH_TOKEN}");
        await value.fill("Bearer abc");
        await page.waitForFunction(() => document.querySelector('#modal .lib-editor .lib-ag[data-agent="agy"]')?.getAttribute("aria-disabled") === null);
        await value.fill("Bearer ${GH_TOKEN}");
        await page.waitForFunction(() => document.querySelector('#modal .lib-editor .lib-ag[data-agent="agy"]')?.getAttribute("aria-disabled") === "true");
        await page.keyboard.press("Escape");
        await sheet.waitFor({ state: "detached" });

        // a command's environment says the same
        await row("fs").locator(".sub").click();
        await sheet.waitFor();
        await sheet.getByText(hint, { exact: true }).waitFor();
        await sheet.locator(".lib-pair").first().locator("input").nth(1).fill("${ROOT_DIR}");
        await page.waitForFunction(() => document.querySelector('#modal .lib-editor .lib-ag[data-agent="agy"]')?.getAttribute("aria-disabled") === "true");
        assert.deepEqual(await sideways(page), [], "sideways scroll in the command's editor");

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
