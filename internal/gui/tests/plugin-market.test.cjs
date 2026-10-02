// Run with Node's test runner and Playwright on the module path; see README.md.
// The Plugins tab: Discover lists the plugins magpie suggests in two
// sections; a card installs its plugin, then offers its sign-in, which
// opens in the Providers add sheet. A search filters at once and adds what
// npm has. A card opens the plugin's page with its README — no pictures,
// links opened outside. Installed shows why one didn't load, updates one
// and removes one. The providers list's button and the add sheet lead
// here. English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const listings = [
  { package: "@magpie-community/opencode-zed-auth", name: "Zed", icon: "zed", providers: ["zed"], community: true, replaces: "zed",
    summary: { en: "Claude, GPT, Gemini and Grok hosted by Zed, on your Zed plan.", zh: "用 Zed 订阅使用 Zed 托管的 Claude、GPT、Gemini 和 Grok。" },
    npm: { version: "0.1.0", weekly: 120, license: "MIT", repository: "https://github.com/magpie-community/plugins" } },
  { package: "@magpie-community/opencode-qoder-auth", name: "Qoder", icon: "qoder", providers: ["qoder"], community: true,
    summary: { en: "Your Qoder subscription.", zh: "Qoder 订阅。" }, npm: { weekly: 0 } },
  { package: "opencode-copilot-auth", name: "GitHub Copilot", icon: "githubcopilot", providers: ["github-copilot"],
    summary: { en: "Your GitHub Copilot plan.", zh: "你的 GitHub Copilot 套餐。" },
    npm: { version: "0.0.9", weekly: 48213, publisher: "thdxr", license: "MIT", repository: "https://github.com/sst/opencode-copilot-auth" } },
  { package: "opencode-gemini-auth", name: "Gemini", icon: "gemini-color", providers: ["google"],
    summary: { en: "Gemini with a Google account.", zh: "用 Google 账号使用 Gemini。" },
    npm: { version: "1.4.0", weekly: 9100, publisher: "jenslys" } },
];

const README = [
  "# opencode-copilot-auth",
  "![badge](https://img.shields.io/npm/v/x.svg) Copilot for **OpenCode**.",
  "**Curated by [Ann](https://example.test/ann)**",
  "[![npm version](https://img.shields.io/npm/v/x.svg)](https://www.npmjs.com/package/x)",
  "[![Tests](https://github.com/x/y/badge.svg)](https://github.com/x/y/actions)",
  "[Jump](#-install) · <a href=\"https://example.test/html\"><b>HTML link</b></a>",
  ...Array.from({ length: 40 }, (_, i) => "\nFiller paragraph " + i + " so the page scrolls."),
  "",
  "## 🚀 Install",
  "```sh",
  "opencode auth login",
  "```",
  "- one `item`",
  "- see [the docs](https://example.test/docs)",
  "<img src=\"https://evil.test/x.png\"> <script>window.pwned = 1</script>",
].join("\n");

function server(lang, asked) {
  const installed = [{ spec: "opencode-broken", error: "Cannot find module 'x'", providers: [], version: "1.0.0" }];
  const subs = [];
  const market = () => ({ listings, state: { bun: true, bunVersion: "1.3.0", plugins: installed } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }],
        presets: [], excluded: [], gateway: { running: true, window: true }, plugins: subs });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    // the page asks for the market in parts (#488)
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") { const m = market(); return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings: m.listings } : m); }
    if (url.pathname === "/api/plugins/search") {
      asked.push(["search", url.searchParams.get("q")]);
      return json({ hits: [
        { package: "opencode-copilot-auth", version: "0.0.9" },
        { package: "opencode-copilot-proxy-auth", version: "2.1.0", description: "Copilot through a proxy", publisher: "someone" },
      ] });
    }
    if (url.pathname === "/api/plugins/page") { asked.push(["page", url.searchParams.get("name")]); return json({ readme: README, updated: "2026-09-01T00:00:00Z" }); }
    if (url.pathname === "/api/plugins/add") {
      const b = body();
      asked.push(["add", b]);
      await new Promise((r) => setTimeout(r, 150));
      installed.push({ spec: b.spec, providers: ["GitHub Copilot"], version: "0.0.7", latest: "0.0.9" });
      subs.push({ id: "github-copilot-plugin", pid: "github-copilot", name: "GitHub Copilot", icon: "githubcopilot", spec: b.spec, signedIn: false, models: 34,
        methods: [{ type: "oauth", label: "Login with GitHub" }] });
      return json({});
    }
    if (url.pathname === "/api/plugins/upgrade") { asked.push(["upgrade", body()]); installed.find((e) => e.spec === body().spec).version = "0.0.9"; return json({}); }
    if (url.pathname === "/api/plugins/remove") { asked.push(["remove", body()]); installed.splice(installed.findIndex((e) => e.spec === body().spec), 1); return json({}); }
    if (url.pathname === "/api/open") { asked.push(["open", body()]); return route.fulfill({ status: 204 }); }
    if (url.pathname === "/api/plugin-signin") { asked.push(["signin", body()]); return json({ id: "p1", agent: "github-copilot-plugin", state: "waiting", url: "https://github.test/login/device" }); }
    if (url.pathname === "/api/signin/p1") return json({ id: "p1", agent: "github-copilot-plugin", state: "waiting", url: "https://github.test/login/device" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") { asked.push(["fetched", url.href]); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { tab: "Plugins", ours: "magpie community", install: "Install", soon: "Coming soon", signIn: "Sign in", onNpm: "On npm",
    installed: "Installed", failed: /Didn't load: Cannot find module/, update: "Update", remove: "Remove", moreTile: "More in Plugins",
    lookFor: "look for a plugin", week: "48k/week", readme: "Install", signing: "GitHub Copilot" },
  zh: { tab: "插件", ours: "magpie 社区", install: "安装", soon: "即将上线", signIn: "登录", onNpm: "npm 上的插件",
    installed: "已安装", failed: /没有加载成功：Cannot find module/, update: "更新", remove: "移除", moreTile: "插件中还有更多",
    lookFor: "找找插件", week: "48k/周", readme: "Install", signing: "GitHub Copilot" },
};

const shot = async (loc, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await loc.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Plugins tab", async (t) => {
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

        // the providers list has no way of its own there: the add sheet's
        // Subscriptions has it, as its last row
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").waitFor();
        assert.equal(await page.locator("#view-providers .after-list .more-subs").count(), 0);
        await page.locator("#addProvider").click();
        const row = page.locator("#addSheet .tile.more-plugins");
        await row.waitFor();
        await shot(page.locator("#addSheet"), `plugins-row-${engine}-${lang}`);
        await row.click();
        const view = page.locator("#view-plugins");
        await view.waitFor({ state: "visible" });
        assert.equal(await page.locator('#nav button[data-view="plugins"]').getAttribute("class"), "on");
        assert.match(page.url(), /view=plugins/);

        // Discover: magpie's community's plugins alone, others' only found by a search
        await view.locator(".pm-sechead h3", { hasText: w.ours }).waitFor();
        assert.equal(await view.locator(".pm-sec").count(), 1);
        assert.equal(await view.locator('.pm-card[data-pkg="opencode-copilot-auth"]').count(), 0);
        assert.doesNotMatch(await view.innerText(), /OpenCode 社区|OpenCode's community/);
        assert.equal(await view.locator('.pm-card[data-pkg="@magpie-community/opencode-qoder-auth"] .pm-act').innerText(), w.soon);
        assert.ok(await view.locator('.pm-card[data-pkg="@magpie-community/opencode-qoder-auth"] .pm-act').isDisabled());
        await shot(view, `plugins-discover-${engine}-${lang}`);
        await view.locator(".pm-find input").fill("copilot");
        const copilot = view.locator('.pm-card[data-pkg="opencode-copilot-auth"]');
        await copilot.locator(".pm-dl", { hasText: w.week }).waitFor();
        assert.equal((await copilot.locator(".pm-by").innerText()).trim(), "thdxr");

        // a card's page: facts, links opened outside, the README without pictures
        await copilot.locator(".pm-sum").click();
        const dlg = page.locator("#modal .pm-detail");
        await dlg.locator(".pm-md h4", { hasText: w.readme }).waitFor();
        assert.equal(await dlg.locator(".pm-md img, .pm-md script").count(), 0);
        assert.equal(await page.evaluate(() => window.pwned), undefined);
        assert.doesNotMatch(await dlg.locator(".pm-md").innerText(), /pwned/);
        assert.equal(await dlg.locator(".pm-md pre code").innerText(), "opencode auth login");
        await shot(page.locator("#modal .dialog"), `plugins-page-${engine}-${lang}`);
        // markdown in bold is still markdown; a badge (a picture in a link) goes whole; nothing shows as source
        assert.equal((await dlg.locator(".pm-md strong a").innerText()).trim(), "Ann");
        assert.doesNotMatch(await dlg.locator(".pm-md").innerText(), /!\[|\]\(|npm version|Tests|<\/?[ab]\b/);
        assert.equal((await dlg.locator(".pm-md a", { hasText: "HTML link" }).innerText()).trim(), "HTML link");
        // a link to a heading goes to it, in the page, not out
        const body = dlg.locator(".ebody");
        assert.equal(await body.evaluate((b) => b.scrollTop), 0);
        await dlg.locator(".pm-md a", { hasText: "Jump" }).click();
        await page.waitForFunction(() => {
          const b = document.querySelector("#modal .pm-detail .ebody"), h = b.querySelector("h4[data-slug='-install']");
          const r = h?.getBoundingClientRect(), v = b.getBoundingClientRect();
          return h && b.scrollTop > 0 && r.top >= v.top - 1 && r.bottom <= v.bottom + 1;
        });
        assert(!asked.some(([k]) => k === "open"), "a heading's link isn't opened outside");
        await dlg.locator(".pm-md a", { hasText: "the docs" }).click();
        assert.deepEqual(asked.find(([k]) => k === "open")[1], { url: "https://example.test/docs" });

        // install from the page: the button becomes Sign in, which opens the add sheet's sign-in
        await dlg.locator(".pm-act", { hasText: w.install }).click();
        await dlg.locator(".pm-act.go", { hasText: w.signIn }).waitFor();
        assert.deepEqual(asked.find(([k]) => k === "add")[1], { spec: "opencode-copilot-auth" });
        await dlg.locator(".pm-act.go").click();
        await page.locator("#view-providers").waitFor({ state: "visible" });
        await page.waitForFunction(() => document.querySelector("#modal").hidden);
        await page.locator("#addSheet .signing").waitFor();
        assert.match(await page.locator("#addSheet .signing").innerText(), new RegExp(w.signing));

        // the add sheet's row to Plugins, and its way when a search finds nothing
        const tile = page.locator("#addSheet .tile.more-plugins");
        await page.locator("#addSheet .signing button.text:not(.primary)").last().click().catch(() => {});
        await tile.waitFor();
        assert.equal((await tile.innerText()).trim(), w.moreTile);
        await page.locator("#addSheet input.find").fill("fakeco");
        await page.locator("#addSheet .none button", { hasText: w.lookFor }).click();
        await view.waitFor({ state: "visible" });
        assert.equal(await view.locator(".pm-find input").inputValue(), "fakeco");

        // a search: the list filtered at once, and what npm has
        const q = view.locator(".pm-find input");
        await q.fill("copilot");
        await view.locator(".pm-sechead h3", { hasText: w.onNpm }).waitFor();
        await view.locator('.pm-card[data-pkg="opencode-copilot-proxy-auth"]').waitFor();
        assert.equal(await view.locator('.pm-card[data-pkg="opencode-copilot-auth"]').count(), 1, "a listed plugin isn't shown twice");
        assert.ok(asked.some(([k, v]) => k === "search" && v === "copilot"));
        assert.equal(await q.evaluate((e) => e === document.activeElement), true, "typing keeps the field");
        await shot(view, `plugins-search-${engine}-${lang}`);
        await q.fill("");

        // Installed: why one didn't load, an update, a removal
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const rows = view.locator(".pm-row");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2);
        await view.locator(".pm-row .sub.bad", { hasText: w.failed }).waitFor();
        const cp = rows.filter({ hasText: "GitHub Copilot" });
        await cp.locator(".pm-chip.up").waitFor();
        await shot(view, `plugins-installed-${engine}-${lang}`);
        const top = await view.evaluate((e) => e.scrollTop);
        await cp.locator("button", { hasText: new RegExp("^" + w.update + "$") }).click();
        await cp.locator(".pm-chip.up").waitFor({ state: "detached" });
        assert.deepEqual(asked.find(([k]) => k === "upgrade")[1], { spec: "opencode-copilot-auth" });
        await rows.filter({ hasText: "opencode-broken" }).locator("button", { hasText: w.remove }).click();
        await page.waitForFunction(() => document.querySelectorAll("#view-plugins .pm-row").length === 1);
        assert.equal(await view.evaluate((e) => e.scrollTop), top, "a click doesn't move the page");

        assert.deepEqual(asked.filter(([k]) => k === "fetched"), [], "nothing fetched from elsewhere");
        assert.deepEqual(errors, []);
      });
    }
  });
}
