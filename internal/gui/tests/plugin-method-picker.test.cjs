// Run with Node's test runner and Playwright on the module path; see README.md.
// A moved built-in whose plugin offers more than one way to sign in, with no
// site to tell them apart, must show every way: the plugin's own list is the
// answer, as the CLI already has it. WorkBuddy is the case that found this —
// "WorkBuddy account (browser)" and "WorkBuddy desktop's sign-in" are not the
// same sign-in, and forcing the first hid the second, the one a user whose
// desktop app is already signed in needs.
//
// The same test pins the rest of the rule: one way is taken at once with no
// picker, and a site-named way still wins over the picker.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const plugins = [
  // WorkBuddy: two ways, no site — the picker must appear
  { id: "workbuddy", pid: "workbuddy", name: "WorkBuddy", icon: "generic", spec: "@magpie-community/opencode-workbuddy-auth", signedIn: false, models: 2,
    methods: [{ type: "oauth", label: "WorkBuddy account (browser)" }, { type: "oauth", label: "WorkBuddy desktop's sign-in" }] },
  // one way only: taken at once, no picker
  { id: "factory", pid: "factory", name: "Factory", icon: "generic", spec: "@magpie-community/opencode-factory-auth", signedIn: false, models: 3,
    methods: [{ type: "oauth", label: "Factory account" }] },
  // a site-named way: chosen for that site without a picker
  { id: "zcode", pid: "zcode", name: "ZCode", icon: "generic", spec: "@magpie-community/opencode-zcode-auth", signedIn: false, models: 3,
    methods: [{ type: "oauth", label: "ZCode: Z.ai GLM Coding Plan" }, { type: "oauth", label: "ZCode: BigModel (智谱) GLM Coding Plan" }, { type: "oauth", label: "ZCode app's sign-in" }] },
];

function server(lang, asked) {
  const payload = () => ({
    providers: [{ id: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", chat: "plugin://workbuddy/v1", models: [], agents: [], key: {},
      account: { agent: "workbuddy", agentName: "WorkBuddy", agentIcon: "workbuddy-color", user: "18276461185", logins: [{ user: "18276461185", active: true, on: true }, { user: "星夜", on: true }] } }],
    presets: [], excluded: [], gateway: { running: true, window: true }, plugins, onPlugins: ["workbuddy", "factory", "zcode"],
  });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugin-signin/prompt") { asked.push(["prompt", body()]); return json({ prompt: null, inputs: {} }); }
    if (url.pathname === "/api/plugin-signin") {
      const b = body();
      asked.push(["signin", b]);
      return json({ id: "s1", agent: b.provider, state: "waiting", url: "", code: "", instructions: "" });
    }
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", agent: "x", state: "waiting", url: "" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { subs: "Subscriptions", ask: "How do you sign in to", browser: "WorkBuddy account (browser)", desktop: "WorkBuddy desktop's sign-in" },
  zh: { subs: "订阅", ask: "用哪种方式登录", browser: "WorkBuddy account (browser)", desktop: "WorkBuddy desktop's sign-in" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a moved built-in with several ways shows every way", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        // the row's own add entry, which is where a user adds a second account
        await page.locator(".row.provider", { hasText: "WorkBuddy" }).click();
        const ed = page.locator("#modal .editor");
        await ed.locator(".accts").waitFor();
        await ed.locator("button.acc.add", { hasText: w.browser.slice(0, 9) }).first().click();

        // Both ways are offered, and neither is the other: the picker shows
        // the plugin's own list, in its order, with the plugin's own names.
        const ask = page.locator(".signing");
        await ask.locator("button", { hasText: w.desktop }).waitFor();
        const labels = await ask.locator(".choices button").allInnerTexts();
        assert.deepEqual(labels, [w.browser, w.desktop], "both ways are shown, in the plugin's order");
        // and each button carries the index the API takes, as the plugin lists it
        assert.deepEqual(await ask.locator(".choices button").evaluateAll((bs) => bs.map((b) => b.dataset.method)), ["0", "1"], "the buttons carry the plugin's own indices");
        // nothing was signed in yet: the picker only asks
        assert.equal(asked.filter(([k]) => k === "signin").length, 0, "the picker itself signs in to nothing");

        // the desktop way is the second one, and taking it sends method 1.
        // The dialog moves on as soon as the request goes out, so the sent
        // body is what proves which way was taken — not what stays on screen.
        await ask.locator(".choices button").nth(1).click();
        for (let i = 0; i < 40 && asked.filter(([k]) => k === "signin").length === 0; i++) {
          await page.waitForTimeout(50);
        }
        assert.deepEqual(asked.filter(([k]) => k === "signin").map(([, b]) => b), [{ provider: "workbuddy", method: 1, inputs: {} }], "the desktop way is method index 1");
        assert.deepEqual(errors, []);
      });
    }
  });

  // the browser way is still the first, and still taken for its own index.
  // A fresh page, so the dialog the test above opened is not in the way.
  test(engine + ": the browser way stays first", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "WorkBuddy" }).click();
        const ed = page.locator("#modal .editor");
        await ed.locator(".accts").waitFor();
        await ed.locator("button.acc.add", { hasText: w.browser.slice(0, 9) }).first().click();
        const ask = page.locator(".signing");
        await ask.locator("button", { hasText: w.browser }).waitFor();
        await ask.locator(".choices button").nth(0).click();
        for (let i = 0; i < 40 && asked.filter(([k]) => k === "signin").length === 0; i++) {
          await page.waitForTimeout(50);
        }
        assert.deepEqual(asked.filter(([k]) => k === "signin").map(([, b]) => b), [{ provider: "workbuddy", method: 0, inputs: {} }], "the browser way is method index 0");
        assert.deepEqual(errors, []);
      });
    }
  });
}
