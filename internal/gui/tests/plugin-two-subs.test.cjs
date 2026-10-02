// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin that signs in to two subscriptions (Qoder and Qoder CN,
// WorkBuddy AI and WorkBuddy), one of them signed in (#556: the Installed
// row's blue Sign in beside "Qoder CN: not signed in" read as the plugin
// signed out while Providers said signed in): the row's button names the
// other one and isn't the primary one, and Discover's card says Signed
// in. A plugin signed in to nothing keeps its blue Sign in. English and
// Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const QODER = "@magpie-community/opencode-qoder-auth", ZED = "@magpie-community/opencode-zed-auth";
const listings = [
  { package: QODER, name: "Qoder", icon: "qoder", providers: ["qoder", "qoder-cn"], community: true, summary: { en: "Qoder.", zh: "Qoder。" }, npm: { version: "0.2.0", weekly: 10 } },
  { package: ZED, name: "Zed", icon: "zed", providers: ["zed"], community: true, summary: { en: "Zed.", zh: "Zed。" }, npm: { version: "0.1.0", weekly: 10 } },
];
const subs = [
  { id: "qoder", pid: "qoder", name: "Qoder", icon: "qoder", spec: QODER, signedIn: true, models: 2, methods: [{ type: "oauth", label: "Qoder" }] },
  { id: "qoder-cn", pid: "qoder-cn", name: "Qoder CN", icon: "qoder", spec: QODER, signedIn: false, models: 2, methods: [{ type: "oauth", label: "Qoder CN" }] },
  { id: "zed", pid: "zed", name: "Zed", icon: "zed", spec: ZED, signedIn: false, models: 3, methods: [{ type: "oauth", label: "Zed" }] },
];

function server(lang, asked) {
  const state = { bun: true, bunVersion: "1.3.0", plugins: [
    { spec: QODER, providers: ["Qoder", "Qoder CN"], version: "0.2.0" },
    { spec: ZED, providers: ["Zed"], version: "0.1.0" },
  ] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      return json({ providers: [{ id: "qoder", name: "Qoder", icon: "qoder", models: [], agents: [], key: {}, account: { agent: "qoder", agentName: "Qoder", agentIcon: "qoder", user: "a@q", logins: [{ user: "a@q", active: true, on: true }] } }],
        presets: [], excluded: [], gateway: { running: true, window: true }, plugins: subs });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") return json(url.pathname === "/api/plugins" ? state : url.pathname === "/api/plugins/listings" ? { listings } : { listings, state });
    if (url.pathname === "/api/plugin-signin/prompt") { asked.push(["prompt", route.request().postDataJSON()]); return json({ prompt: null, inputs: {} }); }
    if (url.pathname === "/api/plugin-signin") { asked.push(["signin", route.request().postDataJSON()]); return json({ id: "s1", agent: "qoder-cn", state: "waiting", url: "https://fake.test/device" }); }
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", agent: "qoder-cn", state: "waiting", url: "https://fake.test/device" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", signIn: "Sign in", other: "Sign in to Qoder CN", signedIn: "Signed in", tip: /Qoder CN is a subscription of its own; Qoder works without it/ },
  zh: { installed: "已安装", signIn: "登录", other: "登录 Qoder CN", signedIn: "已登录", tip: /Qoder CN 是另一个独立的订阅，Qoder 不需要它也能用/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin signed in to one of its two subscriptions", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");

        // Discover: the card of the one signed in says so
        const card = view.locator(`.pm-card[data-pkg="${QODER}"] .pm-act`);
        await card.locator("span", { hasText: w.signedIn }).waitFor();
        assert.ok(await card.evaluate((b) => b.classList.contains("done")));
        assert.equal((await view.locator(`.pm-card[data-pkg="${ZED}"] .pm-act`).innerText()).trim(), w.signIn);

        // Installed: the row names the other, quietly
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const qoder = view.locator(".pm-row").filter({ hasText: "Qoder CN" });
        await qoder.waitFor();
        const b = qoder.locator(".val button", { hasText: w.other });
        assert.equal(await b.count(), 1, "the row's Sign in names Qoder CN");
        assert.ok(!(await b.evaluate((x) => x.classList.contains("primary"))), "not the row's primary button");
        assert.match(await qoder.locator(".pm-sub:not(.in)").getAttribute("title"), w.tip);
        // signed in to nothing: the blue Sign in stays
        const zed = view.locator(".pm-row").filter({ hasText: "Zed" });
        const zb = zed.locator(".val button.primary");
        assert.equal((await zb.innerText()).trim(), w.signIn);
        if (process.env.ARTIFACT_DIR) await view.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-two-subs-${engine}-${lang}.png`) });

        // and it opens that one's sign-in
        await b.click();
        await page.locator("#addSheet .signing").first().waitFor();
        assert.match(await page.locator("#addSheet .signing").first().innerText(), /Qoder CN/);
        assert.deepEqual(errors, []);
      });
    }
  });
}
