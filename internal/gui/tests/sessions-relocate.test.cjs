// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude Code project folder renamed or moved (#1131): its row on the
// Sessions page has Relocate project in its ⋯ menu, which opens magpie's
// own dialog (never prompt()) with the old path and a field for the new one. Preview posts
// sessions/relocate-claude with no token and says what would move; the same
// button then confirms with the preview's token, and editing the path first
// asks for a new preview. A folder with a session magpie can't write, or a
// gateway folder, has no Relocate. In every language, Chromium and WebKit,
// at a narrow width; no backend, the API is faked here. The new-path field
// spans the dialog, its label on one line above it, at the reporter's
// 1320px window as well as narrow ones (#1991, beansiayo).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const atPointer = async (page, target, menu) => {
  const b = await target.boundingBox(), vw = page.viewportSize().width;
  const px = b.x + b.width / 2, py = b.y + b.height / 2;
  // the menu's own placement, not its bounding box, which an entrance transform scales
  const m = await menu.evaluate((e) => ({ x: parseFloat(e.style.left), y: parseFloat(e.style.top), w: e.offsetWidth, h: e.offsetHeight }));
  // opens at the pointer, or pushed in to the window's edge (mmmg_btc on X)
  const x = Math.max(8, Math.min(px - 4, vw - m.w - 8));
  assert(Math.abs(m.x - x) <= 2, `menu at x=${m.x}, expected ${x} for a pointer at ${px}`);
  // below the pointer, or above it where there is no room below
  assert((m.y >= py - 2 && m.y <= py + 14) || (m.y + m.h >= py - 14 && m.y + m.h <= py + 2), `menu at y=${m.y}-${m.y + m.h}, pointer at ${py}`);
};
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min, extra = {}) => ({
  agent: "claude", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  resume: `cd ${cwd} && claude --resume ${id}`, path: `~/.claude/projects/x/${id}.jsonl`, size: 1024, messages: 4, files: 1, deletable: true, ...extra,
});

function serve(lang, calls) {
  let moved = false;
  const list = () => moved
    ? [sess("a-1", "/work/new-app", "fix the login", 30), sess("a-2", "/work/new-app", "add tests", 90), sess("d-1", "/work/desk", "desktop chat", 120, { read_only: true, deletable: false })]
    : [sess("a-1", "/work/old-app", "fix the login", 30), sess("a-2", "/work/old-app", "add tests", 90), sess("d-1", "/work/desk", "desktop chat", 120, { read_only: true, deletable: false })];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "claude", count: 3, deletable: true, name: "Claude Code", icon: "claude" }],
        agent: "claude", sessions: list(), terminal: false, trash: [], trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/relocate-claude") {
      const body = req.postDataJSON();
      calls.push(body);
      const out = { from: body.from, to: body.to, sessions: 2, files: 3, bytes: 2048, token: "tk-" + body.to };
      if (!body.token) return json(out);
      if (body.token !== out.token) return json({ error: "project files changed; preview the relocation again" }, 400);
      moved = true;
      return json({ ...out, backup: "/home/me/.claude/magpie-relocations/project-1" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const navs = { en: "Sessions", zh: "会话", "zh-TW": "會話", ja: "セッション", de: "Sitzungen" };
const keys = ["Relocate project", "Relocate Claude Code project", "New absolute project path", "Preview relocation", "Confirm relocation",
  "Close Claude Code and its editor sessions first. This moves the entire project's sessions, not just filtered or selected rows. Existing target storage is never overwritten. Desktop-only sessions are not changed.",
  "Relocation complete. Original files and recovery manifest: {dir}",
  "Move {n} sessions ({size}) from {from} to {to}. Originals are retained outside Claude's project list. Historical message text is unchanged."];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1320, 1100, 900, 420]) {
      test(`${engine} ${lang} ${width}px: a Claude Code folder is relocated after a preview, in magpie's own dialog`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sessions-relocate.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        const calls = [];
        await page.route("**/*", serve(lang, calls));
        await page.goto("http://magpie.test/?view=providers");
        const nav = page.locator("#nav").getByRole("button", { name: navs[lang], exact: true });
        if (await nav.isVisible()) await nav.click();
        else await page.evaluate(() => show("sessions"));
        const view = page.locator("#view-sessions");
        const tr = (k, vars) => page.evaluate(([k, vars]) => t(k, vars), [k, vars]);
        const folder = (name) => view.locator(".row.sm-folder").filter({ has: page.locator(".fold .name", { hasText: new RegExp("^" + name + "$") }) });
        await folder("old-app").waitFor();

        // every string has its translation
        if (lang !== "en") {
          const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang][k]), [lang, keys]);
          assert.deepEqual(missing, [], `${lang} has every string`);
        }

        // Relocate is in the folder's ⋯ menu; a read-only folder has none
        const relocate = await tr("Relocate project");
        assert.equal(await folder("desk").locator(".sm-folder-more").count(), 0, "no Relocate where a session is read-only");
        const more = folder("old-app").locator(".sm-folder-more");
        await more.scrollIntoViewIfNeeded();
        await folder("old-app").hover();
        const box = await more.boundingBox();
        assert(box && box.x + box.width <= width, "the ⋯ fits in the row");
        // a right-click on the row opens the same menu at the pointer, not under the ⋯ (mmmg_btc on X)
        const nameAt = folder("old-app").locator(".fold .name");
        await nameAt.click({ button: "right" });
        await page.locator(".row-menu").waitFor();
        await atPointer(page, nameAt, page.locator(".row-menu"));
        await page.keyboard.press("Escape");
        await page.locator(".row-menu").waitFor({ state: "detached" });
        const open0 = await folder("old-app").locator(".fold").getAttribute("aria-expanded");
        const y0 = await page.evaluate(() => document.querySelector("#view-sessions").scrollTop);
        await more.click();
        const move = page.locator(".row-menu").getByRole("menuitem", { name: relocate, exact: true });
        await move.click();
        const ask = page.locator("#modal .sm-ask");
        await ask.waitFor();
        assert.equal(await page.evaluate(() => document.querySelector("#view-sessions").scrollTop), y0, "the click held the page still");
        assert.equal(await folder("old-app").locator(".fold").getAttribute("aria-expanded"), open0, "Relocate didn't unfold the folder");
        assert.equal((await ask.locator(".ehead b").textContent()).trim(), await tr("Relocate Claude Code project"));
        assert.equal((await ask.locator(".sm-ask-path").textContent()).trim(), "/work/old-app");
        const over = await ask.evaluate((e) => e.scrollWidth > e.clientWidth || e.getBoundingClientRect().right > innerWidth);
        assert.equal(over, false, "the dialog fits the window");

        const input = ask.locator("input[type=text]");
        assert.equal(await input.inputValue(), "/work/old-app");
        // #1991: the label was squeezed into the form's 72px label column,
        // its text broken over two lines and the path field 72px wide
        const field = await ask.evaluate((ed) => {
          const label = ed.querySelector("label"), input = label.querySelector("input");
          const range = document.createRange();
          range.selectNodeContents(label.firstChild);
          const lines = new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size;
          const box = ed.getBoundingClientRect(), i = input.getBoundingClientRect(), text = range.getBoundingClientRect();
          return { lines, inputW: i.width, edW: box.width, gap: i.top - text.bottom };
        });
        assert.equal(field.lines, 1, "the label's text is on one line");
        assert(field.inputW >= field.edW - 60, `the path field spans the dialog (${field.inputW} of ${field.edW})`);
        assert(field.gap >= 4, `the label stands clear of the field (${field.gap}px)`);
        const go = ask.locator("button.primary");
        assert.equal((await go.textContent()).trim(), await tr("Preview relocation"));
        await input.fill("/work/new-app");
        await go.click();
        await page.waitForFunction((x) => document.querySelector("#modal .sm-ask button.primary")?.textContent === x, await tr("Confirm relocation"));
        assert.deepEqual(calls, [{ from: "/work/old-app", to: "/work/new-app", wsl: "", token: "" }]);
        const info = ask.locator("p.lib-confirm").last();
        assert((await info.textContent()).includes("/work/new-app"), "the preview names the new path");

        // editing the path asks for a new preview, never confirms the old one
        await input.fill("/work/newer-app");
        assert.equal((await go.textContent()).trim(), await tr("Preview relocation"));
        await input.fill("/work/new-app");
        await go.click();
        await page.waitForFunction((x) => document.querySelector("#modal .sm-ask button.primary")?.textContent === x, await tr("Confirm relocation"));
        await go.click();
        await page.waitForFunction(() => document.querySelector("#modal .sm-ask button.primary")?.hidden);
        assert.equal(calls.length, 3);
        assert.deepEqual(calls[2], { from: "/work/old-app", to: "/work/new-app", wsl: "", token: "tk-/work/new-app" });
        assert.equal((await info.textContent()).trim(), await tr("Relocation complete. Original files and recovery manifest: {dir}", { dir: "/home/me/.claude/magpie-relocations/project-1" }));
        await folder("new-app").waitFor();
        assert.equal(await folder("old-app").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
