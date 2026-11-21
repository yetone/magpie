// Run with Node's test runner and Playwright on the module path; see README.md.
// nf1qaz2w on Discord: each of an MCP server's listed tools has a switch.
// One the library has turned off shows off (dimmed); a click flips that
// switch at once, sends the tools turned off, opens no editor and scrolls
// nothing; quick clicks go as one request once the first is answered. The
// agents given the server that can't leave out a single tool (toolsOff
// unset in their view) are named under the list, never shown as following
// the switches. In every language, both engines, a wide and a narrow window.
// Opening the list leaves the row's chips and switch where they were
// (the owner). No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/nf";
const fixture = (off) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    { id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, toolsOff: true },
    { id: "cursor", name: "Cursor", icon: "cursor", skills: `${HOME}/.cursor/skills`, mcp: `${HOME}/.cursor/mcp.json` },
    { id: "goose", name: "Goose", icon: "goose", mcp: `${HOME}/.config/goose/config.yaml` },
    { id: "codex", name: "Codex", icon: "codex-color", mcp: `${HOME}/.codex/config.toml`, toolsOff: true },
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
  servers: [
    { name: "filesystem", transport: "stdio", command: "npx", args: ["-y", "@modelcontextprotocol/server-filesystem", HOME], agents: ["claude", "codex", "cursor", "goose"], ...(off.length ? { disabledTools: off } : {}) },
    { name: "bare", transport: "stdio", command: "bare-mcp", agents: ["claude"] },
    ...Array.from({ length: 6 }, (_, i) => ({ name: "more" + i, transport: "stdio", command: "more-mcp", agents: ["claude"] })),
  ],
});
const tools = [
  { name: "read_text_file", description: "Read the complete contents of a file from the file system as text. Handles various text encodings and provides detailed error messages if the file cannot be read." },
  { name: "write_file", description: "Create a new file or completely overwrite an existing file with new content." },
  { name: "delete_a_file_with_a_very_long_name_that_has_no_spaces_in_it_at_all" },
];
const found = { filesystem: { state: "ok", tools: 3, toolList: tools }, bare: { state: "ok", tools: 0 }, ...Object.fromEntries(Array.from({ length: 6 }, (_, i) => ["more" + i, { state: "ok", tools: 0 }])) };

function server(lang, sends) {
  let off = ["write_file"];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: fixture(off) });
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names;
      return route.fulfill({ json: { servers: Object.fromEntries((names?.length ? names : Object.keys(found)).map((n) => [n, found[n]])) } });
    }
    if (url.pathname === "/api/library/servers/tools") {
      const body = req.postDataJSON();
      let release;
      const held = new Promise((r) => (release = r));
      sends.push({ ...body, release });
      await held;
      off = body.names;
      return route.fulfill({ json: { ...fixture(off), changed: ["claude", "codex"], problems: [] } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { label: (x) => `${x} on`, cant: "Cursor, Goose can't turn off single tools: they get every tool of the server.", saved: "Tools of filesystem saved" },
  zh: { label: (x) => `启用 ${x}`, cant: "Cursor, Goose 不能单独关闭工具：它们仍会拿到这个服务器的全部工具。", saved: "已保存 filesystem 的工具设置" },
  "zh-TW": { label: (x) => `啟用 ${x}`, cant: "Cursor, Goose 無法單獨關閉工具：它們仍會取得這個伺服器的全部工具。", saved: "已儲存 filesystem 的工具設定" },
  ja: { label: (x) => `${x} を有効にする`, cant: "Cursor, Goose はツールを個別にオフにできません。このサーバーのツールはすべて使われます。", saved: "filesystem のツール設定を保存しました" },
  de: { label: (x) => `${x} eingeschaltet`, cant: "Cursor, Goose können einzelne Tools nicht ausschalten: Sie bekommen alle Tools des Servers.", saved: "Tools von filesystem gespeichert" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [980, 420]) {
      test(`${engine} ${lang} ${width}: each of an MCP server's tools has a switch`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        const errors = [];
        t.after(async () => { await browser.close(); });
        const w = words[lang], sends = [];
        const ctx = await browser.newContext({ viewport: { width, height: width < 600 ? 820 : 500 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("http://magpie.test/**", server(lang, sends));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
        const pill = page.locator('#view-library .lib-health[data-server="filesystem"]');
        await page.waitForFunction(() => document.querySelector('#view-library .lib-health[data-server="filesystem"]')?.classList.contains("has-list"));
        const list = page.locator('#view-library .lib-tools[data-server="filesystem"]');
        // the row's chips and switch: where each is, from the row's top, and
        // whether it is beside the name's column or on a line under it
        const marks = () => page.evaluate(async () => {
          // after the frame's layout and resize observers, as it is painted
          await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
          const row = document.querySelector('#view-library .lib-row[data-server="filesystem"]'), who = row.querySelector(":scope > .who");
          const r = row.getBoundingClientRect(), w = who.getBoundingClientRect(), box = who.querySelector(".lib-tools");
          return { under: box.hidden ? null : box.getBoundingClientRect().bottom - r.top, at: [...row.children].filter((c) => c !== who).map((c) => {
            const b = c.getBoundingClientRect();
            return { cls: c.className, top: b.top - r.top, beside: b.top < w.bottom };
          }) };
        });
        const closed = await marks();
        assert.ok(closed.at.length >= 2, "the row has its chips and switch");
        const level = (now, why) => closed.at.forEach((c, i) => {
          // beside the name they stay put; under it they go under the list
          if (c.beside || now.under == null) assert.ok(Math.abs(now.at[i].top - c.top) <= 1, `${why}: ${c.cls} moved from ${c.top} to ${now.at[i].top}`);
          else assert.ok(now.at[i].top >= now.under - 1, `${why}: ${c.cls} at ${now.at[i].top} is over the list (to ${now.under})`);
        });
        await pill.click();
        await list.waitFor();
        level(await marks(), "opened");
        // closed again, all back where they were; then open for the rest
        await pill.click();
        await list.waitFor({ state: "hidden" });
        level(await marks(), "closed again");
        await pill.click();
        await list.waitFor();
        level(await marks(), "opened again");

        // a switch beside each tool, off for the one the library turned off
        const sw = (name) => list.locator(`.lib-switch[data-tool="${name}"]`);
        const state = () => list.locator("li").evaluateAll((lis) => lis.map((li) => [li.querySelector(".lib-toolname").textContent, li.querySelector('[role="switch"]')?.getAttribute("aria-checked"), li.classList.contains("off")]));
        assert.deepEqual(await state(), [[tools[0].name, "true", false], [tools[1].name, "false", true], [tools[2].name, "true", false]]);
        assert.equal(await sw("write_file").getAttribute("aria-label"), w.label("write_file"));
        // the agents that can't follow them are named; those that can aren't
        assert.equal(await list.locator(".lib-toolcant").textContent(), w.cant);

        // scrolled a little, as the reader would, before the clicks, with
        // every switch in sight below the library's stuck head. Clicking the
        // pill can scroll it into view first (WebKit, now and then), which
        // left read_text_file under the head and the click on the head
        await page.mouse.move(Math.min(490, width / 2), 250);
        await page.mouse.wheel(0, 40);
        await page.waitForFunction(() => document.querySelector("#view-library").scrollTop > 0);
        const placed = () => page.evaluate(() => {
          const v = document.querySelector("#view-library").getBoundingClientRect();
          const head = document.querySelector("#view-library .lib-head").getBoundingClientRect();
          const sws = [...document.querySelectorAll('#view-library .lib-tools[data-server="filesystem"] .lib-switch')].map((b) => b.getBoundingClientRect());
          if (sws.some((r) => r.top < head.bottom)) return -1;
          if (sws.some((r) => r.bottom > v.bottom)) return 1;
          return 0;
        });
        for (let i = 0; i < 30; i++) {
          await page.waitForTimeout(150);
          const p = await placed();
          if (!p) break;
          await page.mouse.wheel(0, 40 * p);
        }
        assert.equal(await placed(), 0, "the switches in sight");
        await page.waitForTimeout(400);
        const scrollTop = () => page.evaluate(() => document.querySelector("#view-library").scrollTop);
        const where = () => sw(tools[2].name).evaluate((b) => b.getBoundingClientRect().top);
        const top = await scrollTop(), at = await where();

        // a click turns one off at once and sends it
        await sw(tools[2].name).click();
        for (let i = 0; i < 50 && !sends.length; i++) await page.waitForTimeout(20);
        assert.equal(sends.length, 1);
        assert.deepEqual(sends[0].names, [tools[2].name, "write_file"]);
        assert.equal(sends[0].name, "filesystem");
        assert.equal(await sw(tools[2].name).getAttribute("aria-checked"), "false");
        assert.equal(await page.evaluate(() => document.querySelector("#modal").classList.contains("lib")), false, "the click opened the editor");
        assert.equal(await where(), at, "the click moved the switch");
        assert.ok(Math.abs((await scrollTop()) - top) <= 1, "the click scrolled the page");
        // two more while the first is out: one request when it's answered
        await sw("write_file").click();
        await sw("read_text_file").click();
        assert.deepEqual(await state(), [[tools[0].name, "false", true], [tools[1].name, "true", false], [tools[2].name, "false", true]]);
        await page.waitForTimeout(100);
        assert.equal(sends.length, 1, "a click sent while one was out");
        sends[0].release();
        for (let i = 0; i < 50 && sends.length < 2; i++) await page.waitForTimeout(20);
        assert.deepEqual(sends[1].names, [tools[2].name, "read_text_file"]);
        sends[1].release();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w.saved);
        assert.deepEqual(await state(), [[tools[0].name, "false", true], [tools[1].name, "true", false], [tools[2].name, "false", true]]);
        assert.equal(sends.length, 2);
        assert.equal(await list.isVisible(), true);

        // it fits the window, with no stripe down its side, and no <select>
        const fit = await page.evaluate(() => {
          const v = document.querySelector("#view-library"), box = document.querySelector('.lib-tools[data-server="filesystem"]').getBoundingClientRect();
          const s = [...document.querySelectorAll('.lib-tools[data-server="filesystem"] .lib-switch')].map((x) => x.getBoundingClientRect().right);
          return { over: v.scrollWidth - v.clientWidth, right: box.right, w: innerWidth, sw: Math.max(...s) };
        });
        assert.ok(fit.over <= 0 && fit.right <= fit.w && fit.sw <= fit.right, "wider than the window: " + JSON.stringify(fit));
        for (const sel of [".lib-tool", ".lib-toolcant"]) {
          const side = await list.locator(sel).first().evaluate((b) => { const s = getComputedStyle(b); return [s.borderLeftWidth, s.borderLeftStyle]; });
          assert.ok(side[0] === "0px" || side[1] === "none", sel + " has a left border");
        }
        assert.equal(await page.locator("#view-library select").count(), 0);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  }
}
