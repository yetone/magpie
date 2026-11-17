// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity's accounts serve requests through magpie only; the app keeps
// its own sign-in (Greatrobbort on X: the preferred account and smart routing
// didn't change the account Antigravity itself uses). Its editor says so,
// with Routing on and off, and never that Antigravity signs in to the first.
// Codex, whose own sign-in magpie does move, doesn't say it.
// Chromium and WebKit, every language, a wide and a narrow window; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const users = ["one@example.com", "two@example.com"];
const sub = (id, name, icon, agentName, routing) => ({
  id, name, icon, models: [], agents: [], fallback: [], headers: {}, keyList: [], ...(routing ? { routing } : {}),
  account: { agent: id, agentName, user: users[0], plan: "pro", logins: users.map((user, i) => ({ user, plan: "pro", active: i === 0, on: true })) },
});
let providers = [];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/providers") return json({ providers, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const says = {
  en: ["Only requests through magpie use them; Antigravity itself stays signed in as it is.", "The gateway uses the first."],
  zh: ["只有经过 magpie 的请求会用这些账号；Antigravity 本身的登录不变。", "网关"],
  "zh-TW": ["只有經過 magpie 的請求會用這些帳號；Antigravity 本身的登入不變。", "閘道"],
  ja: ["これらのアカウントを使うのは magpie を通るリクエストだけです。Antigravity 自身のサインインはそのままです。", "ゲートウェイ"],
  de: ["Nur Anfragen über magpie verwenden diese Konten; Antigravity selbst bleibt angemeldet wie bisher.", "Zugangspunkt"],
};
const signsIn = { en: "Antigravity signs in to the first" };

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
for (const engine of engines) {
  for (const lang of Object.keys(says)) {
    for (const width of [1000, 560]) {
      test(`${engine} ${lang} ${width}px: Antigravity's Accounts say the app keeps its own sign-in`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        for (const routing of ["smart", ""]) {
          providers = [sub("antigravity", "Antigravity", "antigravity-color", "Antigravity", routing), sub("codex", "Codex", "openai", "Codex", "smart")];
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "Antigravity" }).first().click();
          await page.locator(".editor .acc").first().waitFor();
          const text = await page.locator(".editor").innerText();
          if (routing) assert.ok(text.includes(says[lang][0]), `with Routing, Antigravity's editor doesn't say the app keeps its sign-in:\n${text}`);
          else assert.ok(text.includes(says[lang][1]), `without Routing, Antigravity's editor doesn't name the gateway:\n${text}`);
          if (signsIn[lang]) assert.ok(!text.includes(signsIn[lang]), "Antigravity's editor says the app signs in to the first");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "the editor is wider than the window");
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "Codex" }).first().click();
          await page.locator(".editor .acc").first().waitFor();
          assert.ok(!(await page.locator(".editor").innerText()).includes(says[lang][0].replace("Antigravity", "Codex")), "Codex's editor says its own sign-in stays");
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
