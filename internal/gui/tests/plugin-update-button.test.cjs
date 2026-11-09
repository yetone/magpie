// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's Update is a button the size of Install, in a colour of its own
// (#1587, timonwong: 有插件更新提示，但是更新按钮不明显): on its Discover
// card beside another card's Install, on its Installed row, and Update all
// under the list. Chromium and WebKit, every language, at a narrow width;
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  const installed = [
    { spec: "opencode-copilot-auth", providers: ["GitHub Copilot"], version: "0.0.7", latest: "0.0.9" },
  ];
  const listings = [
    { package: "opencode-copilot-auth", name: "Copilot", icon: "copilot", providers: ["github-copilot"], community: true, summary: { en: "Copilot", zh: "Copilot" }, npm: { version: "0.0.9" } },
    { package: "opencode-other-auth", name: "Other", icon: "other", providers: ["other"], community: true, summary: { en: "Other", zh: "Other" }, npm: { version: "1.0.0" } },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/updates") return json({ checked: "2026-10-01T00:00:00Z", waiting: [], updated: [] });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") { const m = { listings, state: { bun: true, bunVersion: "1.3.0", plugins: installed } }; return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings } : m); }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { update: "Update", install: "Install", installed: "Installed", all: "Update all (1)" },
  zh: { update: "更新", install: "安装", installed: "已安装", all: "全部更新（1）" },
  "zh-TW": { update: "更新", install: "安裝", installed: "已安裝", all: "全部更新（1）" },
  ja: { update: "アップデート", install: "インストール", installed: "インストール済み", all: "すべてアップデート（1）" },
  de: { update: "Aktualisieren", install: "Installieren", installed: "Installiert", all: "Alle aktualisieren (1)" },
};

// what makes a button's size and look
const look = (b) => b.evaluate((e) => {
  // offset sizes, as laid out: a list's entrance scale is not the button's size
  const s = getComputedStyle(e);
  return { h: e.offsetHeight, w: e.offsetWidth, minW: s.minWidth, pad: s.paddingLeft + " " + s.paddingRight, font: s.fontSize + " " + s.fontWeight, radius: s.borderRadius, margin: s.margin, bg: s.backgroundColor, ring: s.boxShadow, color: s.color };
});

function sameSize(got, want, what) {
  assert.equal(got.h, want.h, what + ": height");
  assert.equal(got.minW, want.minW, what + ": min width");
  assert.equal(got.pad, want.pad, what + ": padding");
  assert.equal(got.font, want.font, what + ": font");
  assert.equal(got.radius, want.radius, what + ": corners");
  assert.equal(got.margin, want.margin, what + ": margins (the title bar's .update pill isn't its look)");
  assert.ok(got.w >= parseFloat(want.minW), what + ": at least Install's width");
  // Install's ring, its word green: told apart by its colour alone
  assert.equal(got.bg, want.bg, what + ": Install's fill");
  assert.equal(got.ring, want.ring, what + ": Install's ring");
  assert.notEqual(got.color, want.color, what + ": its text a colour of its own");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's Update is the size of Install, in a colour of its own", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 520, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");

        // Discover: Copilot's card offers Update, Other's Install
        const up = view.locator('.pm-card[data-pkg="opencode-copilot-auth"] button.pm-act.pm-up').first();
        const get = view.locator('.pm-card[data-pkg="opencode-other-auth"] button.pm-act.get').first();
        await up.waitFor();
        await get.waitFor();
        assert.equal((await up.textContent()).trim(), w.update);
        assert.equal((await get.textContent()).trim(), w.install);
        const install = await look(get);
        const card = await look(up);
        sameSize(card, install, "card");
        // nothing else on the card says Update in a smaller way
        assert.equal(await view.locator('.pm-card[data-pkg="opencode-copilot-auth"] .pm-chip.up').count(), 0);
        // the whole button is in the card, at this width too
        const fits = await up.evaluate((e) => { const c = e.closest(".pm-card").getBoundingClientRect(), r = e.getBoundingClientRect(); return r.left >= c.left && r.right <= c.right; });
        assert.ok(fits, "the card's Update fits in its card");

        // Installed: its row's Update and Update all
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const row = view.locator(".pm-row", { hasText: "GitHub Copilot" });
        const rowUp = row.locator("button.pm-act.pm-up");
        await rowUp.waitFor();
        assert.equal((await rowUp.textContent()).trim(), w.update);
        const r = await look(rowUp);
        sameSize(r, install, "row");
        assert.equal(r.bg, card.bg, "the row's Update looks as the card's does");
        const all = view.locator(".pm-updates button.pm-act.pm-up");
        await all.waitFor();
        assert.equal((await all.textContent()).trim(), w.all);
        const a = await look(all);
        sameSize(a, install, "Update all");
        assert.equal(a.bg, card.bg, "Update all looks as the card's Update does");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  });
}
