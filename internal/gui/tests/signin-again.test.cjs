// Run with Node's test runner and Playwright on the module path; see README.md.
// A sign-in to an account magpie lists already is said to be one (#413:
// WorkBuddy's page offers the account WorkBuddy is signed in to, and adding
// it read as if a new one came in, or as nothing at all). Add another
// WorkBuddy account, the sign-in comes back done with again: the status
// says the account is already listed and its sign-in renewed, not "added";
// a new one still says added. No click moves the page. English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const account = {
  id: "workbuddy", name: "WorkBuddy (CodeBuddy)", icon: "workbuddy-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5", name: "GLM-5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "workbuddy", agentName: "WorkBuddy", user: "Alice", logins: [{ user: "Alice", active: true, on: true, own: true }, { user: "Bob", on: true }] },
};

function serve(lang, done) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [account], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/signin") return json({ id: "wb1", agent: "workbuddy", state: "waiting", url: "https://www.codebuddy.cn/login?x=1" });
    if (url.pathname === "/api/signin/wb1") return json({ id: "wb1", agent: "workbuddy", state: "done", ...done.shift() });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const want = {
  en: { again: "Bob is already listed — its sign-in was renewed", added: "Carol added — switch to it any time" },
  zh: { again: "Bob：该账号已在列表中，已更新登录", added: "已添加 Carol，随时可以切换" },
};

const cases = [
  { name: "an account listed already is said to be", done: { user: "Bob", again: true }, say: "again" },
  { name: "a new account is said to be added", done: { user: "Carol" }, say: "added" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const c of cases) {
      test(`${engine} ${lang}: ${c.name}`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${c.say}-signin-again.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, [c.done]));
        // the vendor's sign-in page, opened in a new window, gets an empty page:
        // the real one is a page the test can't control (ChatGPT's Cloudflare
        // challenge hung clicks on this page in Chromium, #1307)
        await page.context().route((url) => url.hostname !== "magpie.test", (route) => route.fulfill({ contentType: "text/html", body: "" }));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "WorkBuddy" }).click();
        const add = page.locator(".editor .accts .acc.add").first();
        await add.waitFor();
        const scrolls = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
        const before = await scrolls();
        await add.click();
        const say = want[lang][c.say];
        await page.waitForFunction((s) => document.querySelector("#status").textContent.includes(s), say);
        assert.equal((await page.locator("#status").textContent()).trim(), say);
        assert.deepEqual(await scrolls(), before, "nothing scrolled");
        assert.deepEqual(errors, []);
      });
    }
  }
}
