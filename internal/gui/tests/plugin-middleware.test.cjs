// Run with Node's test runner and Playwright on the module path; see README.md.
// Gateway middleware plugins on the Installed tab: a plugin that is only
// middleware says what it runs (its hooks, calls, µs each, failures with
// the last error in the tip) where a subscription plugin says what it
// signs in to, and never "Signs in to nothing magpie can use"; one that
// didn't load says why in red; one switched off says Off alone; a plugin
// that is both keeps its sign-in line and gains the middleware line.
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const ALIAS = "/Users/x/mw/alias.middleware.js", BROKEN = "/Users/x/mw/broken.middleware.js", OFF = "/Users/x/mw/off.middleware.js", BOTH = "/Users/x/mw/both";
const state = { bun: true, bunVersion: "1.3.0", plugins: [
  { spec: ALIAS, providers: [], moved: [], isMiddleware: true, middlewareOnly: true, optionsExample: { mapping: { fast: "deepseek-chat" } }, middleware: { hooks: ["onRequest", "onEvent"], events: ["content_block_delta"], calls: 12345, avgMicros: 0.93, failures: 2, lastError: "onEvent: TypeError: x is undefined" } },
  { spec: BROKEN, providers: [], moved: [], isMiddleware: true, middlewareOnly: true, middleware: { hooks: [], error: "exports none of onRequest, onEvent and onResponse", calls: 0, avgMicros: 0, failures: 0 } },
  { spec: OFF, off: true, providers: [], moved: [], isMiddleware: true, middlewareOnly: true },
  { spec: BOTH, providers: ["Acme"], moved: [], isMiddleware: true, middleware: { hooks: ["onResponse"], calls: 0, avgMicros: 0, failures: 0 } },
] };

function server(lang, pluginState = state) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") return json(url.pathname === "/api/plugins" ? pluginState : url.pathname === "/api/plugins/listings" ? { listings: [] } : { listings: [], state: pluginState });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", chip: "Middleware", provider: "Provider", line: "Gateway middleware: onRequest, onEvent · 12,345 calls, 0.9 µs each", failed: "2 failed", last: /Last: onEvent: TypeError: x is undefined/, events: /onEvent sees only content_block_delta events/, broken: /^Middleware didn't load: exports none/, off: "Off", nothing: "Signs in to nothing", both: "Signs in to Acme", bothLine: "Gateway middleware: onResponse" },
  zh: { installed: "已安装", chip: "中间件", provider: "供应商", line: "网关中间件：onRequest、onEvent · 调用 12,345 次，平均每次 0.9 µs", failed: "2 次失败", last: /最近一次：onEvent: TypeError: x is undefined/, events: /onEvent 只处理 content_block_delta 事件/, broken: /^中间件没有加载：exports none/, off: "关闭", nothing: "不能登录", both: "Acme", bothLine: "网关中间件：onResponse" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": gateway middleware plugins in Installed", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const row = (name) => view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: name }) });

        const alias = row("alias.middleware.js");
        await alias.waitFor();
        const kinds = (r) => r.locator(".pm-chip.kind").allInnerTexts().then((a) => a.map((x) => x.trim()));
        assert.deepEqual(await kinds(alias), [w.chip]);
        const line = alias.locator(".pm-mw");
        assert.equal((await line.innerText()).replace(/\s+/g, " ").trim(), w.line + " · " + w.failed);
        assert.match(await line.getAttribute("title"), w.events);
        assert.match(await line.locator(".pm-mw-fail").getAttribute("title"), w.last);
        assert.equal(await line.locator(".pm-mw-fail").evaluate((e) => getComputedStyle(e).color), await page.evaluate(() => { const d = document.createElement("span"); d.style.color = "var(--red)"; document.body.append(d); const c = getComputedStyle(d).color; d.remove(); return c; }));
        assert.ok(!(await alias.innerText()).includes(w.nothing), "a middleware plugin isn't said to sign in to nothing");

        const broken = row("broken.middleware.js");
        assert.match((await broken.locator(".pm-mw").innerText()).trim(), w.broken);
        assert.ok(await broken.locator(".pm-mw").evaluate((e) => e.classList.contains("bad")));
        assert.deepEqual(await kinds(broken), [w.chip], "one that didn't load is middleware still");

        const off = row("off.middleware.js");
        assert.equal((await off.locator(".who .sub").innerText()).trim(), w.off);
        assert.equal(await off.locator(".pm-mw").count(), 0);
        assert.deepEqual(await kinds(off), [w.chip], "one switched off is middleware still, not a provider");

        const both = row("both");
        assert.ok((await both.locator(".who .sub").first().innerText()).includes(w.both));
        assert.equal((await both.locator(".pm-mw").innerText()).trim(), w.bothLine);
        assert.deepEqual(await kinds(both), [w.chip, w.provider]);

        // nothing drawn with a coloured left border
        const borders = await view.locator(".pm-row *").evaluateAll((els) => els.filter((e) => { const s = getComputedStyle(e); return parseFloat(s.borderLeftWidth) > 0 && s.borderLeftStyle !== "none" && parseFloat(s.borderRightWidth) === 0; }).length);
        assert.equal(borders, 0);
        if (process.env.ARTIFACT_DIR) await view.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-middleware-${engine}-${lang}.png`) });
        assert.deepEqual(errors, []);
      });
    }
  });

  // Discover says plugins can be middleware too, not only subscriptions,
  // and links to how one is written in the reader's language
  test(engine + ": Discover's intro names middleware", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const I = {
      en: { h: "Subscriptions and gateway middleware", p: /middleware in magpie's gateway/, a: "Write a middleware", url: "https://usemagpie.ai/docs/plugins#middleware", trust: /sign-in or your requests/ },
      zh: { h: "订阅与网关中间件", p: /magpie 网关里的中间件/, a: "编写中间件", url: "https://usemagpie.ai/docs/zh/plugins#middleware", trust: /登录或看到你的请求/ },
    };
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = I[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], opened = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.route("**/api/open", (r) => { opened.push(r.request().postDataJSON()); return r.fulfill({ json: {} }); });
        await page.goto("http://magpie.test/?view=plugins");
        const intro = page.locator("#view-plugins .pm-intro");
        await intro.waitFor();
        assert.equal((await intro.locator("h2").innerText()).trim(), w.h);
        assert.match(await intro.innerText(), w.p);
        assert.match(await intro.locator(".pm-trust").innerText(), w.trust);
        const y = await page.evaluate(() => [document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
        await intro.locator("a.pm-link", { hasText: w.a }).click();
        await page.waitForTimeout(100);
        assert.deepEqual(opened, [{ url: w.url }]);
        assert.equal(await page.evaluate(() => [document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join()), y, "the click scrolled nothing");
        if (process.env.ARTIFACT_DIR) await intro.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-intro-${engine}-${lang}.png`) });
        assert.deepEqual(errors, []);
      });
    }
  });

  // a middleware's options are edited as JSON in its row: what its package
  // suggests when none are set, JSON that isn't an object said so without
  // a post, and Save posts the object for its spec
  test(engine + ": a middleware's options in its row", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const O = {
      en: { installed: "Installed", options: "Options", save: "Save", note: "Not set: these are its package's example", bad: /^Not JSON: /, obj: "Options are a JSON object" },
      zh: { installed: "已安装", options: "选项", save: "保存", note: "未设置：这是包里给的示例", bad: /^不是 JSON：/, obj: "选项须是一个 JSON 对象" },
    };
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = O[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const pluginState = structuredClone(state);
        await page.route("**/*", server(lang, pluginState));
        await page.route("**/api/plugins/options", (r) => {
          const body = r.request().postDataJSON();
          posted.push(body);
          pluginState.plugins[0].options = body.options;
          return r.fulfill({ json: pluginState });
        });
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const row = (name) => view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: name }) });
        const alias = row("alias.middleware.js");
        await alias.waitFor();
        assert.equal(await row("broken.middleware.js").locator("button", { hasText: w.options }).count(), 0, "no Options on one that didn't load");
        assert.equal(await row("off.middleware.js").locator("button", { hasText: w.options }).count(), 0, "no Options on one switched off");
        const y = await page.evaluate(() => [document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
        await alias.locator("button", { hasText: w.options }).click();
        const ta = alias.locator(".pm-opts textarea");
        await ta.waitFor();
        assert.equal(await ta.getAttribute("aria-label"), w.options);
        assert.equal(await page.evaluate(() => [document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join()), y, "the click scrolled nothing");
        assert.deepEqual(JSON.parse(await ta.inputValue()), { mapping: { fast: "deepseek-chat" } });
        assert.equal((await alias.locator(".pm-opts-bar > span").innerText()).trim(), w.note);
        assert.equal(await view.locator("select").count(), 0);
        await ta.fill("{ mapping: ");
        await alias.locator(".pm-opts button", { hasText: w.save }).click();
        assert.match((await alias.locator(".pm-opts-err").innerText()).trim(), w.bad);
        assert.equal(await ta.getAttribute("aria-invalid"), "true");
        assert.equal(await alias.locator(".pm-opts-err").getAttribute("role"), "alert");
        await alias.locator(".pm-opts textarea").fill("[1]");
        await alias.locator(".pm-opts button", { hasText: w.save }).click();
        assert.equal((await alias.locator(".pm-opts-err").innerText()).trim(), w.obj);
        assert.deepEqual(posted, []);
        await alias.locator(".pm-opts textarea").fill('{"mapping": {"fast": "glm-5"}}');
        await alias.locator(".pm-opts button", { hasText: w.save }).click();
        await alias.locator(".pm-opts").waitFor({ state: "detached" });
        assert.deepEqual(posted, [{ spec: ALIAS, options: { mapping: { fast: "glm-5" } } }]);
        await alias.getByRole("button", { name: w.options, exact: true }).click();
        const clear = alias.getByRole("button", { name: lang === "zh" ? "清空" : "Clear", exact: true });
        await clear.click();
        const confirm = page.getByRole("alertdialog");
        await confirm.waitFor();
        assert.match(await confirm.textContent(), lang === "zh" ? /不带选项运行/ : /run with no options/);
        assert.equal(posted.length, 1, "Clear waits for confirmation");
        await confirm.getByRole("button", { name: lang === "zh" ? "取消" : "Cancel", exact: true }).click();
        assert.equal(posted.length, 1, "Cancel keeps the saved options");
        assert.deepEqual(JSON.parse(await ta.inputValue()), { mapping: { fast: "glm-5" } });
        await clear.click();
        await confirm.locator("button").last().click();
        await alias.locator(".pm-opts").waitFor({ state: "detached" });
        assert.deepEqual(posted[1], { spec: ALIAS, options: null });
        const borders = await view.locator(".pm-row *").evaluateAll((els) => els.filter((e) => { const s = getComputedStyle(e); return parseFloat(s.borderLeftWidth) > 0 && s.borderLeftStyle !== "none" && parseFloat(s.borderRightWidth) === 0; }).length);
        assert.equal(borders, 0);
        assert.deepEqual(errors, []);
      });
    }
  });

  // Discover keeps subscriptions and middleware apart, and each card says
  // which it is
  test(engine + ": Discover's cards say provider or middleware", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const listings = [
      { package: "@magpie-community/opencode-zed-auth", name: "Zed", providers: ["zed"], community: true, summary: { en: "Zed's models.", zh: "Zed 的模型。" }, npm: { version: "0.1.0" } },
      { package: "@magpie-community/middleware-model-map", name: "Model map", kind: "middleware", community: true, summary: { en: "Model redirection.", zh: "模型重定向。" }, npm: { version: "0.1.0" } },
    ];
    const D = {
      en: { subs: "Subscriptions", mw: "Gateway middleware", pv: "Provider", mwc: "Middleware" },
      zh: { subs: "订阅", mw: "网关中间件", pv: "供应商", mwc: "中间件" },
    };
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = D[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.route("**/api/plugins/listings", (r) => r.fulfill({ json: { listings } }));
        await page.route("**/api/plugins/market", (r) => r.fulfill({ json: { listings, state } }));
        await page.goto("http://magpie.test/?view=plugins");
        const secs = page.locator("#view-plugins .pm-sec");
        await secs.first().waitFor();
        const heads = (await secs.locator(".pm-sechead h3").allInnerTexts()).map((x) => x.trim());
        assert.deepEqual(heads, [w.subs, w.mw]);
        const card = (n) => page.locator("#view-plugins .pm-card").filter({ hasText: n });
        assert.equal((await card("Zed").locator(".pm-chip.kind").innerText()).trim(), w.pv);
        assert.equal((await card("Model map").locator(".pm-chip.kind").innerText()).trim(), w.mwc);
        assert.equal(await secs.nth(1).locator(".pm-card").count(), 1);
        if (process.env.ARTIFACT_DIR) await page.locator("#view-plugins").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-kinds-${engine}-${lang}.png`) });
        assert.deepEqual(errors, []);
      });
    }
  });
}
