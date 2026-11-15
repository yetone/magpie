// Run with Node's test runner and Playwright on the module path; see README.md.
// Ethereal499, Isdavexxx and MasterYi (#540): the Codex app greys its Send
// while the ChatGPT account it is signed in to is used up. magpie becomes
// Codex's provider for that account and restarts the app's app-server, so
// the app sends again but turns its ChatGPT side off till the reset. The
// Agents row says so: on magpie until when, and (on hover and in the row
// opened) what is off and that it all comes back. When magpie couldn't
// restart the app-server, it says to quit and reopen the app. What is off
// names browser use, which the Codex app's Chrome control loses with
// magpie its provider, and the row opened offers Keep on ChatGPT right
// there, posted as the sign-in's Always ChatGPT (eddie.zhan on Discord:
// browser use said it had no permission). It names the phone connection
// too: the Codex app ties "Connect your phone" to its ChatGPT account, which
// it doesn't read while magpie is the provider (Mugenrikka on X: the phone
// stopped connecting and the profile was gone, with nothing saying why or
// that Keep on ChatGPT has them back). Every language, both engines,
// wide and narrow. No backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const until = new Date(Date.now() + 3 * 3600e3).toISOString();
const codex = (appOut) => ({
  id: "codex", name: "Codex", path: "/fixture/config.toml", wired: true,
  fields: [{ key: "model", label: "model", value: "sub/gpt-6.1-sol", options: [{ value: "sub/gpt-6.1-sol", label: "sub/gpt-6.1-sol", ref: "sub/gpt-6.1-sol" }] }],
  appOut,
});

const words = {
  en: { until: "On magpie until", reset: "On magpie until the reset", stuck: "quit and reopen the Codex app", off: "cloud tasks", browser: "browser use", phone: "its phone connection", keep: "Keep on ChatGPT" },
  zh: { until: "由 magpie 接管至", reset: "由 magpie 接管至额度重置", stuck: "退出并重新打开 Codex 应用", off: "云端任务", browser: "浏览器操作", phone: "手机连接", keep: "保持 ChatGPT" },
  "zh-TW": { until: "由 magpie 接管至", reset: "由 magpie 接管至額度重設", stuck: "結束並重新開啟 Codex 應用", off: "雲端任務", browser: "瀏覽器操作", phone: "手機連線", keep: "保持 ChatGPT" },
  ja: { until: "まで magpie が処理", reset: "リセットまで magpie が処理", stuck: "Codex アプリを終了して開き直して", off: "クラウドタスク", browser: "ブラウザ操作", phone: "スマートフォンとの接続", keep: "ChatGPT のまま" },
  de: { until: "Über magpie bis", reset: "Über magpie bis zum Zurücksetzen", stuck: "beende die Codex-App", off: "Cloud-Aufgaben", browser: "Browser-Nutzung", phone: "Verbindung zum Handy", keep: "Bei ChatGPT bleiben" },
};

function serve(lang, agents, sets = []) {
  const state = { agents, profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/set") {
      sets.push(route.request().postDataJSON());
      return route.fulfill({ json: { ...state, agents: agents.map((a) => ({ ...a, appOut: undefined })) } });
    }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="codex"]';
const cases = [
  ["until the reset's time", { user: "spent@example.com", until }, "until"],
  ["until the reset, its time unknown", { user: "spent@example.com" }, "reset"],
  ["the app-server couldn't be restarted", { user: "spent@example.com", until, stuck: true }, "stuck"],
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [980, 440]) {
      test(`${engine} ${lang} ${width}px: the Codex row says it is on magpie till the reset, and what is off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const [name, appOut, kind] of cases) {
          await t.test(name, async () => {
            const page = await browser.newPage({ viewport: { width, height: 800 } });
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            const sets = [];
            await page.route("**/*", serve(lang, [codex(appOut)], sets));
            await page.goto("http://magpie.test/?view=agents");
            const st = page.locator(`${row} .ag-st-t`);
            await st.waitFor();
            const text = await st.textContent();
            assert.ok(text.includes(w[kind]), `${JSON.stringify(text)} lacks ${JSON.stringify(w[kind])}`);
            assert.doesNotMatch(text, /\{\w+\}/);
            const title = await page.locator(`${row} .ag-st-t`).getAttribute("title");
            assert.ok(title && title.includes(w.off) && title.includes(w.browser) && title.includes(w.phone), "the hover says what is off, browser use and the phone connection too: " + title);
            assert.ok(title.includes(w.keep), "the hover names Keep on ChatGPT: " + title);
            assert.ok(title.includes("spent@example.com"), "the hover names the account: " + title);
            assert.doesNotMatch(title, /\{\w+\}/);

            await page.locator(`${row} .ag-link`).click();
            const said = page.locator(`${row} .ag-app-out`);
            await said.waitFor();
            const s = await said.textContent();
            assert.ok(s.includes(w.off) && s.includes(w.browser) && s.includes(w.phone), "the row opened says what is off, browser use and the phone connection too: " + s);
            assert.doesNotMatch(s, /\{\w+\}/);
            const side = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
            assert.ok(side <= 1, "scrolls sideways: " + side);
            const keep = page.locator(`${row} .ag-app-out + .ag-vl button`);
            assert.equal((await keep.textContent()).trim(), w.keep);
            const box = await keep.boundingBox(), vw = page.viewportSize().width;
            assert.ok(box && box.x >= 0 && box.x + box.width <= vw + 0.5, "Keep on ChatGPT is on the page: " + JSON.stringify(box));
            // the row grows open on a spring: pressed once it has, as a
            // reader would, the button the top thing where it is
            await page.waitForFunction(() => {
              const b = document.querySelector('.row.agent[data-id="codex"] .ag-app-out + .ag-vl button'), r = b.getBoundingClientRect();
              return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2) === b;
            });
            await keep.click();
            await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .ag-app-out'));
            assert.deepEqual(sets, [{ agent: "codex", field: "login", value: "chatgpt" }]);
            assert.deepEqual(errors, []);
            await page.close();
          });
        }
      });
    }
  }
  test(`${engine}: without appOut the row says nothing of it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 980, height: 800 } });
    await page.route("**/*", serve("en", [codex(undefined)]));
    await page.goto("http://magpie.test/?view=agents");
    await page.locator(`${row} .ag-st-t`).waitFor();
    assert.doesNotMatch(await page.locator(`${row} .ag-st-t`).textContent(), /On magpie until|ChatGPT allowance/);
    await page.locator(`${row} .ag-link`).click();
    await page.locator(`${row} .ag-exp`).waitFor();
    assert.equal(await page.locator(`${row} .ag-app-out`).count(), 0);
  });
}
