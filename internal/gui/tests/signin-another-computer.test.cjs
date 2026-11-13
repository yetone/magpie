// Run with Node's test runner and Playwright on the module path; see README.md.
// Wing8Aqua on X: adding a Claude account opened the local browser, and the
// account to add signs in on another computer (another region). Under a
// subscription's accounts, "Sign in on another computer…" starts the
// sign-in kept to its link: /api/signin gets linkOnly, and the window shows
// the link to copy, no page opened here. The ordinary "Add another …" row
// posts no linkOnly. Codex and Claude both have the row; English, Chinese,
// Japanese and German, Chromium and WebKit, 1100 and 420 wide. No backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LINK = "https://claude.example/oauth/authorize?state=abc";
const sub = (id, name) => ({
  id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "m1", name: "M1", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: id, agentName: name, user: "a@example.com", plan: "PRO", logins: [
    { user: "a@example.com", plan: "PRO", active: true, on: true },
    { user: "b@example.com", plan: "PRO", on: true },
  ] },
});
const label = {
  en: ["Sign in on another computer…", "Add another"],
  zh: ["在另一台电脑上登录…", "再添加一个"],
  ja: ["別のコンピュータでサインイン…", "別の"],
  de: ["Auf einem anderen Computer anmelden…", "Weiteres"],
};

function serve(lang, id, name, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [sub(id, name)], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/signin") {
      const body = route.request().postDataJSON();
      posts.push(body);
      return json({ id: "s1", agent: id, state: "waiting", url: LINK, ...(body.linkOnly ? { linkOnly: true } : {}) });
    }
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", agent: id, state: "waiting", url: LINK, linkOnly: true });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(label)) {
      for (const [id, name] of [["claude", "Claude"], ["codex", "Codex"]]) {
        test(`${engine} ${width} ${lang} ${id}: Sign in on another computer posts linkOnly and shows the link`, async (t) => {
          const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
          t.after(() => browser.close());
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], posts = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, id, name, posts));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: name }).first().click();
          await page.locator(".editor .acc").first().waitFor();
          const row = page.locator(".editor .accts .acc.add", { hasText: label[lang][0] });
          assert.equal(await row.count(), 1, "the row is there");
          await row.click();
          await page.locator(".signing .signlink code").waitFor();
          assert.equal(posts.length, 1);
          assert.equal(posts[0].linkOnly, true);
          assert.equal(posts[0].agent, id);
          assert.ok((await page.locator(".signing .signlink code").textContent()).includes("claude.example"));
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
