// Run with Node's test runner and Playwright on the module path; see README.md.
// WorkBuddy's subscriptions (NancyTom on X, 11-04: WorkBuddy accounts used
// through DeepSeek Harness via magpie were banned, the international build's
// most): starting a sign-in first says Tencent may ban the account, and
// nothing is asked of the backend until "Sign in anyway". Both the built-in
// tiles and a subscription moved onto its plugin, which is how every user
// has it now. The window's words are the CLI's (provider.WorkBuddyRisk).
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const plugins = [{ id: "workbuddy-ai", pid: "workbuddy-ai", name: "WorkBuddy AI", icon: "workbuddy-color", spec: "@magpie-community/opencode-workbuddy-auth", signedIn: false, models: 0,
  methods: [{ type: "oauth", label: "WorkBuddy's sign-in" }] }];

function server(lang, asked, moved) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true },
      ...(moved ? { plugins, onPlugins: ["workbuddy-ai"], movable: ["workbuddy", "workbuddy-ai"] } : {}) });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/signin" || url.pathname === "/api/plugin-signin/prompt") {
      asked.push([url.pathname, route.request().postDataJSON()]);
      return json({ id: "s1", agent: "workbuddy-ai", state: "waiting", url: "https://www.workbuddy.ai/login" });
    }
    if (url.pathname.startsWith("/api/signin/") || url.pathname.startsWith("/api/plugin-signin/")) return json({ id: "s1", agent: "workbuddy-ai", state: "waiting" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { cn: "WorkBuddy (CodeBuddy)", intl: "WorkBuddy AI (international)", title: (n) => n + " accounts can be suspended", note: /^Tencent may suspend a WorkBuddy account it sees used outside WorkBuddy\. .*changing an agent's prompt doesn't prevent it/, anyway: "Sign in anyway", cancel: "Cancel" },
  zh: { cn: "WorkBuddy (CodeBuddy)", intl: "WorkBuddy 国际版 (WorkBuddy AI)", title: (n) => n + " 账号可能被封禁", note: /^腾讯可能封禁在 WorkBuddy 之外使用的 WorkBuddy 账号。.*改智能体的提示词也无法避免/, anyway: "仍然登录", cancel: "取消" },
};

test("the window says what the CLI says", async () => {
  const app = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const go = await fs.readFile(path.resolve(__dirname, "../../provider/google.go"), "utf8");
  const js = app.match(/const WORKBUDDY_RISK = ("[^"]+");/)[1];
  const g = go.match(/const WorkBuddyRisk = ("[^"]+")/)[1];
  assert.equal(JSON.parse(js), JSON.parse(g));
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": WorkBuddy's sign-in warns of a ban first", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const [moved, which] of [[false, "cn"], [false, "intl"], [true, "intl"]]) {
        await t.test(lang + (moved ? " moved " : " built-in ") + which, async () => {
          const w = L[lang];
          const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
          const errors = [], asked = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, asked, moved));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#addProvider").click();
          const sheet = page.locator("#addSheet");
          // the tile is named short, the warning in full
          const row = sheet.locator(`.tile[data-pick="${w[which].replace(/\s*\([^()]*\)$/, "")}"]`);
          await row.waitFor();

          await row.click();
          let box = sheet.locator(".signing");
          await box.waitFor();
          await box.locator("button", { hasText: w.cancel }).click();
          await box.waitFor({ state: "detached" });
          assert.equal(asked.length, 0);

          await row.click();
          box = sheet.locator(".signing");
          await box.waitFor();
          assert.equal(await box.locator(".n").textContent(), w.title(w[which]));
          assert.match(await box.locator(".s").textContent(), w.note);
          assert.doesNotMatch(await box.textContent(), /Antigravity|Google/);
          assert.equal(asked.length, 0, "nothing asked before it is said");

          await box.locator("button", { hasText: w.anyway }).click();
          for (let i = 0; i < 50 && !asked.length; i++) await page.waitForTimeout(50);
          assert.equal(asked.length, 1);
          if (moved) assert.deepEqual(asked[0], ["/api/plugin-signin/prompt", { provider: "workbuddy-ai", method: 0, inputs: {} }]);
          else assert.deepEqual(asked[0], ["/api/signin", { agent: which === "cn" ? "workbuddy" : "workbuddy-ai" }]);
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
