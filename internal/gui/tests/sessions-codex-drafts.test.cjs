// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex Desktop makes ~/Documents/Codex/<date>/<name> for each chat started
// without a project, and Codex runs in temp folders too. Those are one Draft
// sessions group after the projects, not one folder each (#5769). Chromium
// and WebKit, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min) => ({
  agent: "codex", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  path: "~/.zcode/cli/db/db.sqlite", size: 1024, messages: 4, files: 1, deletable: true,
});

function serve(lang, calls) {
  let sessions = [
    sess("c_d1", "/home/me/Documents/Codex/2026-08-17/shan", "chat one", 10),
    sess("c_a1", "/home/me/work/app", "fix the login", 20),
    sess("c_d2", "/home/me/Documents/Codex/2026-08-19/ni-s-2", "chat two", 30),
    sess("c_t1", "/private/var/folders/15/p0yq/T", "a probe", 40),
  ];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "codex", count: sessions.length, deletable: true, name: "Codex", icon: "codex" }],
        agent: "codex", home: "/home/me", sessions, terminal: false, trash: [], trashDir: "~/.local/share/magpie/trash/sessions" });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = req.postDataJSON();
      calls.push(body);
      sessions = sessions.filter((s) => !body.ids.includes(s.id));
      return json({ deleted: body.ids, refused: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1000, 420]) {
    test(`${engine} ${width}px: Codex's one-session folders are one Draft sessions group`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve("en", []));
      await page.goto("http://magpie.test/?view=providers");
      await page.evaluate(() => show("sessions"));
      const view = page.locator("#view-sessions");
      const folders = view.locator(".sm-folders .row.sm-folder");
      await folders.first().waitFor();
      assert.deepEqual(await folders.locator(".fold .name").allTextContents(), ["app", "Draft sessions"]);
      assert.equal((await folders.nth(1).locator(".note").last().textContent()).trim(), "3 sessions");
      assert.deepEqual(errors, []);
    });
  }
}
