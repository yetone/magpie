// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's and Codex's Accounts say that switching accounts needs no
// connecting the agent (Greatrobbort on X: "多账号这个我一直都没搞明白到底需要
// 在magpie关联agent不"): magpie moves the agent's own sign-in either way. A
// subscription the gateway alone uses, Copilot here, doesn't say it.
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
const providers = [sub("claude", "Claude", "claudecode-color", "Claude Code"), sub("codex", "Codex", "openai", "Codex", "smart"), sub("copilot", "Copilot", "githubcopilot", "Copilot")];

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
  en: (a) => `${a} needn't be connected to magpie`,
  zh: (a) => `不需要把 ${a} 接入 magpie`,
  "zh-TW": (a) => `不需要把 ${a} 接入 magpie`,
  ja: (a) => `${a} を magpie に接続する必要はありません`,
  de: (a) => `Dafür muss ${a} nicht mit magpie verbunden sein`,
};

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
for (const engine of engines) {
  for (const lang of Object.keys(says)) {
    for (const width of [1000, 560]) {
      test(`${engine} ${lang} ${width}px: Claude Code's and Codex's Accounts say switching needs no connecting`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        for (const [name, agent, shown] of [["Claude", "Claude Code", true], ["Codex", "Codex", true], ["Copilot", "Copilot", false]]) {
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: name }).first().click();
          await page.locator(".editor .acc").first().waitFor();
          const text = await page.locator(".editor").innerText();
          assert.equal(text.includes(says[lang](agent)), shown, `${name}'s editor ${shown ? "doesn't say" : "says"} it needs no connecting`);
          if (shown) assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${name}'s editor is wider than the window`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
