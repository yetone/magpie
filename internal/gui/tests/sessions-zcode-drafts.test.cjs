// Run with Node's test runner and Playwright on the module path; see README.md.
// ZCode runs a session started without a project in a folder it makes for
// that one session (~/.zcode/workspace/draft-<id> or <ms>-<uuid>). Those
// are one Draft sessions group after the projects, not one folder each folded under
// Other folders (flandern1211_58540 on Discord, #5754); a row says its
// folder in its tooltip, and the group's Delete all names no folder. In
// every language, Chromium and WebKit, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min) => ({
  agent: "zcode", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  path: "~/.zcode/cli/db/db.sqlite", size: 1024, messages: 4, files: 1, deletable: true,
});

function serve(lang, calls) {
  let sessions = [
    sess("sess_d1", "/home/me/.zcode/workspace/draft-k3v9", "draft one", 10),
    sess("sess_a1", "/home/me/work/app", "fix the login", 20),
    sess("sess_d2", "/home/me/.zcode/workspace/1791000000000-6182fe70-b234-4422-b716-09e08f8eb0dd", "draft two", 30),
    sess("sess_t1", "/private/var/folders/15/p0yq/T", "a temp one", 40),
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
      return json({ agents: [{ agent: "zcode", count: sessions.length, deletable: true, name: "ZCode", icon: "zcode" }],
        agent: "zcode", home: "/home/me", sessions, terminal: false, trash: [], trashDir: "~/.local/share/magpie/trash/sessions" });
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
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}px: ZCode's draft folders are one Draft sessions group`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sessions-zcode-drafts.png`) });
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
        await page.evaluate(() => show("sessions"));
        const view = page.locator("#view-sessions");
        const tr = (k, vars) => page.evaluate(([k, vars]) => t(k, vars), [k, vars]);
        const folders = view.locator(".sm-folders .row.sm-folder");
        await folders.first().waitFor();

        if (lang !== "en") {
          assert.ok(await page.evaluate((lang) => I18N[lang]["Draft sessions"], lang), `${lang} says Draft sessions`);
        }

        // the project, then the drafts as one group, both in the one list;
        // the project open, the drafts not
        assert.deepEqual(await folders.locator(".fold .name").allTextContents(), ["app", await tr("Draft sessions")]);
        const drafts = folders.nth(1);
        assert.equal((await drafts.locator(".note").last().textContent()).trim(), await tr("{n} sessions", { n: 2 }));
        assert.equal(await drafts.locator(".sm-path").count(), 0, "the group names no folder");
        assert.equal(await drafts.locator(".fold").getAttribute("aria-expanded"), "false");

        // only the temporary folder waits under Other folders
        const others = view.locator(".sm-others");
        await others.locator(".sm-others-fold").click();
        assert.deepEqual(await others.locator(".row.sm-folder .fold .name").allTextContents(), ["T"]);

        // a draft's row says where it ran
        await drafts.locator(".fold").click();
        const d1 = view.locator('.row.sm-sess[data-id="sess_d1"]');
        await d1.waitFor();
        assert.equal(await d1.getAttribute("title"), "/home/me/.zcode/workspace/draft-k3v9");

        // Delete all asks without a folder's path, and deletes both drafts
        await drafts.hover();
        await drafts.locator(".sm-folder-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: await tr("Delete all sessions"), exact: true }).click();
        const ask = page.locator("#modal .sm-ask");
        await ask.waitFor();
        assert.equal(await ask.locator(".sm-ask-path").count(), 0);
        await ask.getByRole("button", { name: await tr("Delete"), exact: true }).click();
        await ask.waitFor({ state: "detached" });
        await page.waitForFunction(() => !document.querySelector('#view-sessions .row.sm-sess[data-id="sess_d1"]'));
        assert.deepEqual(calls.map((c) => [...c.ids].sort()), [["sess_d1", "sess_d2"]]);

        const wide = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
        assert.equal(wide, false, "nothing runs past the window");
        assert.deepEqual(errors, []);
      });
    }
  }
}
