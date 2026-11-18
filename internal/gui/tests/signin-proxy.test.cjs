// Run with Node's test runner and Playwright on the module path; see README.md.
// A sign-in goes through the proxy its account's requests will go through
// (#1663, demystify0137: a sign-in went through the global proxy, the
// account's requests after it through the subscription's). The sign-in
// sheet says which, as a link: Claude's account with a proxy of its own,
// signed in to again (no warning: it has accounts), says that one, and the request
// names the account. The link's menu offers Direct, Claude's proxy (its
// password left out), the account's own and Custom…, but not the global
// proxy, which an account can't be kept on while Claude has a proxy and
// other accounts; picking one starts the sign-in again through it, the one
// before canceled; Custom… asks for the address in place. A subscription
// with no proxy and one account offers the global proxy, ticked. Nothing
// moves the page, the link stays inside the sheet at a phone's width. In
// every language, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SUB = "http://u:secret@10.0.0.1:7890", SHOWN = "http://10.0.0.1:7890", BOB = "socks5://10.0.0.2:1080";
const claude = {
  id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "claude-opus-5", name: "Claude Opus 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "claude", agentName: "Claude Code", user: "alice@example.com", plan: "MAX", logins: [{ user: "alice@example.com", plan: "MAX", active: true, on: true }, { user: "bob@example.com", plan: "PRO", lapsed: true }] },
  proxy: SUB, accountProxies: { "bob@example.com": BOB },
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

// openVia opens the sign-in's proxy menu. The sheet can have just
// scrolled to hold the pressed button, and a click scrolls its target into
// view first: a scroll event is delivered on the next frame, after the
// click has opened the menu, which takes it for the reader's scroll and
// closes. So the button is put in view and that scroll let land first.
async function openVia(page, via) {
  await via.scrollIntoViewIfNeeded();
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await via.click();
}
function serve(lang, seen) {
  const providers = { providers: [claude, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const flows = {};
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/signin") {
      const b = route.request().postDataJSON();
      seen.starts.push(b);
      const id = "s" + seen.starts.length;
      // the server's own pick when none is: the account's, else the
      // subscription's (provider.pickProxy)
      const proxy = b.proxy ?? (b.user === "bob@example.com" ? BOB : b.agent === "claude" ? SUB : "");
      flows[id] = { id, agent: b.agent, state: "waiting", url: "https://vendor.example/oauth?state=" + id, proxy };
      return json(flows[id]);
    }
    const m = url.pathname.match(/^\/api\/signin\/(s\d+)(\/cancel)?$/);
    if (m) {
      if (m[2]) { seen.canceled.push(m[1]); flows[m[1]].state = "canceled"; }
      return json(flows[m[1]]);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { again: "Sign in again", via: "Proxy: {p}", global: "Global proxy", direct: "Direct", custom: "Custom…", go: "Sign in through it" },
  zh: { again: "重新登录", via: "代理：{p}", global: "全局代理", direct: "直连", custom: "自定义…", go: "经由它登录" },
  "zh-TW": { again: "重新登入", via: "代理：{p}", global: "全域代理", direct: "直連", custom: "自訂…", go: "經由它登入" },
  ja: { again: "再度サインイン", via: "プロキシ：{p}", global: "グローバルプロキシ", direct: "直接接続", custom: "カスタム…", go: "これ経由でサインイン" },
  de: { again: "Erneut anmelden", via: "Proxy: {p}", global: "Globaler Proxy", direct: "Direkt", custom: "Eigener…", go: "Darüber anmelden" },
};

const sizes = [{ width: 900, height: 800 }, { width: 380, height: 760 }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const size of sizes) {
      test(`${engine} ${lang} ${size.width}: a sign-in says its proxy and another can be picked`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: size, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${size.width}-signin-proxy.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], seen = { starts: [], canceled: [] };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.goto("http://magpie.test/?view=providers");
        const scrolls = () => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
        const wide = () => page.evaluate(() => document.documentElement.scrollWidth > innerWidth);

        // Bob, signed in to again: through his own proxy, his name sent
        await page.locator(".row.provider", { hasText: "Claude" }).first().click();
        const bob = page.locator(".editor .accts .acc", { hasText: "bob@example.com" });
        await bob.locator("button", { hasText: w.again }).click();
        const box = page.locator(".editor .signing");
        const via = box.locator(".acts .via");
        await via.waitFor();
        assert.deepEqual(seen.starts, [{ agent: "claude", user: "bob@example.com" }]);
        assert.equal((await via.textContent()).trim(), w.via.replace("{p}", BOB));
        const fit = await via.evaluate((v) => { const r = v.getBoundingClientRect(), b = v.closest(".signing").getBoundingClientRect(); return r.left >= b.left - 0.5 && r.right <= b.right + 0.5; });
        assert(fit, "the link stays inside the sheet");
        assert.equal(await wide(), false, "nothing wider than the window");

        // its menu: no global proxy, Claude's without its password, Bob's ticked
        // in sight first: Playwright scrolls to what it clicks
        await via.scrollIntoViewIfNeeded();
        const before = await scrolls();
        await openVia(page, via);
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        const names = await menu.locator(".pm-item .pm-name").allTextContents();
        assert.deepEqual(names, [w.direct, SHOWN, BOB, w.custom]);
        assert(!(await menu.textContent()).includes("secret"), "the proxy's password isn't shown");
        assert.equal(await menu.locator(".pm-item.on .pm-name").textContent(), BOB);
        assert.deepEqual(await scrolls(), before, "opening the menu moved nothing");

        // Claude's proxy picked: the sign-in starts again through it
        await menu.locator(".pm-item", { hasText: SHOWN }).click();
        await page.waitForFunction(() => document.querySelector(".editor .signing .acts .via")?.textContent.includes("10.0.0.1"));
        assert.deepEqual(seen.starts[1], { agent: "claude", user: "bob@example.com", proxy: SUB });
        // the one before is canceled (a request of its own, not awaited)
        for (let i = 0; i < 40 && seen.canceled.length < 1; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.canceled, ["s1"]);
        assert.equal((await via.textContent()).trim(), w.via.replace("{p}", SHOWN));

        // Custom…: its address asked in place, then signed in through. The
        // sign-in started again drew the link anew at the editor's foot,
        // partly under its bar at 380: in sight first, as above
        await via.scrollIntoViewIfNeeded();
        await openVia(page, via);
        await menu.locator(".pm-item", { hasText: w.custom }).click();
        const form = box.locator("form.proxy-ask");
        await form.waitFor();
        const go = form.locator("button", { hasText: w.go });
        assert.equal(await go.isDisabled(), true, "nothing to sign in through yet");
        await form.locator("input").first().fill("http://127.0.0.1:8888");
        await go.click();
        await page.waitForFunction(() => document.querySelector(".editor .signing .acts .via")?.textContent.includes("8888"));
        assert.deepEqual(seen.starts[2], { agent: "claude", user: "bob@example.com", proxy: "http://127.0.0.1:8888" });
        for (let i = 0; i < 40 && seen.canceled.length < 2; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.canceled, ["s1", "s2"]);
        assert.equal(await box.locator("form.proxy-ask").count(), 0, "the address isn't asked again");
        assert.equal(await wide(), false, "nothing wider than the window");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${size.width}: a subscription with no proxy offers the global one`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: size, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], seen = { starts: [], canceled: [] };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        const via = page.locator(".editor .signing .acts .via");
        await via.waitFor();
        assert.deepEqual(seen.starts, [{ agent: "codex" }]);
        assert.equal((await via.textContent()).trim(), w.via.replace("{p}", w.global));
        await openVia(page, via);
        const menu = page.locator(".proto-menu");
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), [w.global, w.direct, w.custom]);
        assert.equal(await menu.locator(".pm-item.on .pm-name").textContent(), w.global);
        // Direct picked: started again with no proxy at all
        await menu.locator(".pm-item", { hasText: w.direct }).click();
        await page.waitForFunction((d) => document.querySelector(".editor .signing .acts .via")?.textContent.includes(d), w.direct);
        assert.deepEqual(seen.starts[1], { agent: "codex", proxy: "direct" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
