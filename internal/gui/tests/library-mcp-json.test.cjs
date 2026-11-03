// Run with Node's test runner and Playwright on the module path; see README.md.
// hema_17384 on Discord: an MCP server's command, an npx with a SQL
// Server's host, user, password and database, was cut off in its one-line
// field. The field wraps now, so the whole line is in view at 390 wide
// too, and the editor shows the server as JSON as well, in Claude Code's
// .mcp.json shape, to read whole and edit: an edit there is what is saved,
// and JSON that isn't a server's keeps the dialog from saving or going
// back to the form.
// youyo.p_10001 on Discord: with many skills and servers, which is which.
// Each takes a note, the library's own: saved with the server or by
// itself for a skill, shown in the row in place of its command or
// description (which the tooltip still gives), found by the skills'
// filter, and asked about before it is lost. Every language, Chromium
// and WebKit, 1100 and 390 wide; nothing scrolls sideways. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const tr = (lang, s) => (lang === "en" ? s : I18N[lang][s]) || s;
const HOME = "/Users/hm";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
// the reporter's line, the password made up
const ARGS = ["-y", "universal-db-mcp", "--type", "sqlserver", "--host", "127.0.0.1", "--port", "1433", "--user", "sa", "--password", "Sh0p-Passw0rd-2026", "--database", "shop_inventory_reporting"];
const LINE = "npx " + ARGS.join(" ");

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code")],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [],
    servers: [{ name: "db", transport: "stdio", command: "npx", args: ARGS, env: { DB_TOKEN: "${DB_TOKEN}" }, agents: ["claude"] }],
    skills: [
      { name: "pdf", description: "Read and fill PDF files", kind: "folder", agents: ["claude"] },
      { name: "weekly", description: "Write a report from the week's commits", kind: "folder", agents: ["claude"] },
      // enough of them for the filter, which shows over eight
      ...["docx", "xlsx", "pptx", "slides", "review", "deploy", "notes"].map((name) => ({ name, description: "Work with " + name, kind: "folder", agents: ["claude"] })),
    ],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/skill") {
      const name = url.searchParams.get("name");
      return route.fulfill({ json: { file: "SKILL.md", files: ["SKILL.md"], text: "---\nname: " + name + "\n---\n\nWrite it up.\n", path: `${HOME}/.magpie/library/skills/${name}` } });
    }
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push(["save", s]);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
    }
    if (url.pathname === "/api/library/skills/note") {
      const b = req.postDataJSON();
      calls.push(["note", b]);
      const s = lib.skills.find((x) => x.name === b.name);
      s.note = b.note.trim();
      return route.fulfill({ json: { ...lib, result: {} } });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an MCP server's command read whole, as JSON, and notes", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s) => tr(lang, s);
          const calls = [];
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          // the MCP tab first; the skills' once the test has picked it
          await ctx.addInitScript(() => { try { if (!localStorage.getItem("magpie.libTab")) localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, calls));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const row = page.locator("#view-library .lib-server", { has: page.locator('.lib-health[data-server="db"]') });
          const ed = page.locator("#modal .lib-editor");
          const cmd = ed.locator("textarea.lib-cmd");
          const json = ed.locator("textarea.lib-json");
          const pick = (name) => ed.locator(".lib-viewpick .opt", { hasText: new RegExp("^" + name + "$") });
          const save = () => ed.locator(":scope > .bar button.primary").click();
          const sideways = () => page.evaluate(() => document.documentElement.scrollWidth > innerWidth || [...document.querySelectorAll("#modal *")].some((e) => getComputedStyle(e).overflowX !== "visible" && e.scrollWidth > e.clientWidth + 1 && e.matches("textarea, input")));
          const open = async () => { await row.locator(".name").click(); await ed.waitFor(); };

          // the reporter's line, whole: wrapped, with nothing past its edges
          await open();
          assert.equal(await cmd.inputValue(), LINE);
          await page.waitForFunction(() => { const c = document.querySelector("#modal textarea.lib-cmd"); return c && c.scrollHeight <= c.clientHeight + 1; });
          const lines = await cmd.evaluate((c) => Math.round(c.clientHeight / parseFloat(getComputedStyle(c).lineHeight)));
          if (width === 390) assert(lines >= 2, "the command wraps at 390 wide: " + lines);
          assert.equal(await sideways(), false, "the command scrolls sideways");
          // its database changed in the box: one line still, Enter saves
          await cmd.fill(LINE.replace("shop_inventory_reporting", "shop"));
          await cmd.press("Enter");
          await ed.waitFor({ state: "detached" });
          assert.deepEqual(calls.at(-1)[1].args, [...ARGS.slice(0, -1), "shop"]);
          assert.equal(calls.at(-1)[1].command, "npx");

          // as JSON: Claude Code's entry, the variable a name
          await open();
          await pick("JSON").click();
          await json.waitFor();
          assert.equal(await ed.locator('input[aria-label="' + w("Name") + '"]').count(), 0, "the form's name is out of the JSON view");
          assert.deepEqual(JSON.parse(await json.inputValue()), { mcpServers: { db: { type: "stdio", command: "npx", args: [...ARGS.slice(0, -1), "shop"], env: { DB_TOKEN: "${DB_TOKEN}" } } } });
          assert.equal(await sideways(), false, "the JSON scrolls sideways");
          // an edit there is what is saved
          const edited = JSON.parse(await json.inputValue());
          edited.mcpServers.db.args.push("--readonly");
          edited.mcpServers.db.env.MODE = "ro";
          await json.fill(JSON.stringify(edited, null, 2));
          // and the form shows it
          await pick(w("Form")).click();
          assert.equal(await cmd.inputValue(), LINE.replace("shop_inventory_reporting", "shop") + " --readonly");
          await pick("JSON").click();
          // broken JSON: no save, no way back to the form, asked before it's lost
          await json.fill('{"mcpServers": {"db": {"command": ');
          assert.equal(await json.getAttribute("aria-invalid"), "true");
          const n = calls.length;
          await save();
          await ed.locator(".editor-error", { hasText: w("This isn't an MCP server's JSON") }).waitFor();
          await pick(w("Form")).click();
          assert.equal(await json.count(), 1, "went back to the form from broken JSON");
          assert.equal(calls.length, n, "broken JSON was saved");
          await json.fill(JSON.stringify(edited));
          assert.equal(await json.getAttribute("aria-invalid"), "false");
          await json.press(process.platform === "darwin" ? "Meta+Enter" : "Control+Enter");
          await ed.waitFor({ state: "detached" });
          assert.deepEqual(calls.at(-1)[1].args, [...ARGS.slice(0, -1), "shop", "--readonly"]);
          assert.deepEqual(calls.at(-1)[1].env, { DB_TOKEN: "${DB_TOKEN}", MODE: "ro" });

          // a note on the server: saved with it, shown in its row, its command in the tooltip
          await open();
          const note = ed.locator("textarea.lib-notein");
          await note.fill("店里的库存数据库，只读");
          await note.press("Enter");
          await ed.waitFor({ state: "detached" });
          assert.equal(calls.at(-1)[1].note, "店里的库存数据库，只读");
          const sub = row.locator(".sub.lib-subnote");
          await sub.waitFor();
          assert.equal(await sub.textContent(), "店里的库存数据库，只读");
          assert((await sub.getAttribute("title")).endsWith(LINE.replace("shop_inventory_reporting", "shop") + " --readonly"));
          assert.equal(await row.locator(".sub.mono").count(), 0);

          // a skill's note, over its SKILL.md
          await page.evaluate(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
          await page.reload();
          await page.locator('button[data-view="library"]').click();
          const skill = (name) => page.locator("#view-library .lib-skill", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
          await skill("weekly").locator(".name").click();
          await ed.waitFor();
          const keep = ed.locator(":scope > .bar button.primary");
          assert.equal(await keep.isVisible(), false, "Save shown with nothing changed");
          await ed.locator("textarea.lib-notein").fill("周报用");
          await keep.waitFor();
          assert.equal(await keep.textContent(), w("Save"));
          // asked before it's lost
          await page.keyboard.press("Escape");
          await page.locator("dialog.action-confirm button:not(.primary)").last().click();
          assert.equal(await ed.count(), 1, "a changed note closed without asking");
          // saved where it is, the skill's file still open; nothing then to ask about
          await ed.locator("textarea.lib-notein").press("Enter");
          await keep.waitFor({ state: "hidden" });
          assert.deepEqual(calls.at(-1), ["note", { name: "weekly", note: "周报用" }]);
          assert.equal(await ed.locator(".lib-skillbody").count(), 1);
          await page.keyboard.press("Escape");
          await ed.waitFor({ state: "detached" });
          const ssub = skill("weekly").locator(".sub.lib-subnote");
          await ssub.waitFor();
          assert.equal(await ssub.textContent(), "周报用");
          assert.equal(await ssub.getAttribute("title"), "周报用\n\nWrite a report from the week's commits");
          // the filter finds it by its note
          await page.locator("#view-library input.lib-skillq").fill("周报");
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-skill").length === 1);
          assert.equal(await skill("weekly").count(), 1);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
