// Run with Node's test runner and Playwright on the module path; see README.md.
// _SekiBetu_ on X: OpenCode was uninstalled and its sessions stayed listed
// with Resume and no Delete. With the agent's command gone the page gives
// its rows no Resume and a Delete whose dialog says the session only leaves
// the list (its own files are not touched); the Trash marks it as taken off
// the list. In every language, Chromium and WebKit; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const ID = "ses_1a2b3c4d5e6fAAAAAAAAAAAAAA";
const DIR = "~/Library/Application Support/magpie/trash/sessions";
const KEYS = {
  confirm: "{agent} isn't installed here. These sessions leave this list; its own files are not touched, and Trash puts them back.",
  off: "Taken off the list",
};

function serve(lang, calls) {
  const store = { sessions: [{ agent: "opencode", id: ID, title: "Flaky test hunt", cwd: "/work/oc", start: at(900), last: at(800), size: 4, messages: 4, files: 1,
    models: [], deletable: true, forget: true, transcript: false, resume: "" }], trash: [] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "opencode", count: store.sessions.length, deletable: true, name: "OpenCode", icon: "generic" }],
        agent: "opencode", sessions: store.sessions, terminal: false, trash: store.trash, trashDir: DIR, recording: false, recorded: false });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = route.request().postDataJSON();
      calls.push(body);
      store.sessions = [];
      store.trash.push({ key: "gateway/20261111-x", agent: "opencode", id: ID, title: "Flaky test hunt", last: at(800), deleted: at(0), size: 0, gateway: true, forgot: true, items: [], name: "OpenCode", icon: "generic" });
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
    test(`${engine} ${lang}: an uninstalled agent's session has a Delete that only takes it off the list`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang, Object.values(KEYS)]);
        assert.deepEqual(missing, [], `every new string has its ${lang}`);
      }
      await page.locator('#nav button[data-view="sessions"]').click();
      const view = page.locator("#view-sessions");
      const row = view.locator(".sm-sess").filter({ hasText: "Flaky test hunt" });
      await row.waitFor();
      assert.equal(await row.locator(".sess-resume").count(), 0, "no Resume for an agent that isn't there");
      assert.equal(await view.locator(".sm-note:not(.sm-speed-note)").count(), 0, "no 'cannot delete' note");
      await row.locator(".sm-del").click();
      const ask = page.locator("#modal .sm-ask");
      await ask.waitFor();
      const said = (await ask.locator(".lib-confirm").allTextContents()).map((x) => x.trim());
      assert.deepEqual(said, [await tr(KEYS.confirm, { agent: "OpenCode" })], "says it only leaves the list");
      await ask.locator(".bar button.primary").click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls, [{ agent: "opencode", ids: [ID] }]);
      await view.locator(".sm-trash-btn").click();
      const trow = view.locator(".sm-trash .row").filter({ hasText: "Flaky test hunt" });
      await trow.waitFor();
      assert((await trow.locator(".sub").textContent()).includes(await tr(KEYS.off)));
      assert.deepEqual(errors, []);
    });
  }
}
