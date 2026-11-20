// Run with Node's test runner and Playwright on the module path; see README.md.
// The add sheet's Partners (yetone: 在订阅上面加上一栏是「合作伙伴」): the
// services that pay to be listed, first and under a heading that says so,
// each one quiet row with no badge of its own. Who they are comes from
// usemagpie.ai through /api/providers (internal/provider/partners.go); one
// that names its languages is listed in those only, and its tagline, in
// the row's title and the editor, is its own in the page's language, else
// its English one. Searching finds one by its tagline. With no partner
// there is no heading. English, Chinese, Japanese and German, at 900px and
// 440px; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const partner = (id, name, extra = {}) => ({ id, name, icon: "generic", kind: "partner", sponsored: true, chat: `https://api.${id}.example.com/v1`, ...extra });
const partners = [
  partner("acme", "Acme AI", { notes: { en: "Fast relay", zh: "快速中转", ja: "高速な中継" },
    // its pages are /go links, named by the host they send the browser to
    website: "https://usemagpie.ai/go/acme/site", siteHost: "www.acme.example",
    regions: [
      { id: "cn", name: "China", chat: "https://api.acme.example.com/v1", website: "https://usemagpie.ai/go/acme/site/cn", siteHost: "cn.acme.example" },
      { id: "global", name: "Global", chat: "https://global.acme.example.com/v1", website: "https://usemagpie.ai/go/acme/site/global", siteHost: "global.acme.example" },
    ], splitAccounts: true }),
  partner("zhonly", "China Only", { langs: ["zh"], notes: { en: "Mainland relay", zh: "国内中转" } }),
  partner("taken", "Taken", { added: true }),
];
const presets = [
  { id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.example.com/v1" },
  { id: "openrouter", name: "OpenRouter", icon: "openai", kind: "relay", chat: "https://openrouter.example.com/v1", sponsored: true },
];
const providers = [{ id: "taken", name: "Taken", icon: "generic", preset: "taken", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang, list, counted) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/api/partner") { counted?.push(route.request().postDataJSON()); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    // a partner the sheet showed is new no more, as provider.NoticePartners keeps
    const shown = new Set((counted || []).filter((c) => c.what === "shown").flatMap((c) => c.ids));
    if (url.pathname === "/api/providers") return json({ providers, presets: [...list.map((p) => (shown.has(p.id) ? { ...p, new: false } : p)), ...presets], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { head: ["Partners", "magpie's sponsors"], subs: "Subscriptions", rows: ["Acme AI", "Taken"], note: "Fast relay", region: "pick the region of your Acme AI account" },
  zh: { head: ["合作伙伴", "赞助 magpie 的服务"], subs: "订阅", rows: ["Acme AI", "China Only", "Taken"], note: "快速中转", region: "按你的 Acme AI 账号所在区域选择" },
  ja: { head: ["パートナー", "magpie のスポンサー"], subs: null, rows: ["Acme AI", "Taken"], note: "高速な中継", region: "Acme AI アカウントのリージョンを選んでください" },
  de: { head: ["Partner", "Sponsoren von magpie"], subs: null, rows: ["Acme AI", "Taken"], note: "Fast relay", region: "die Region deines Acme AI-Kontos wählen" },
};

async function openSheet(page) {
  await page.goto("http://magpie.test/?view=providers");
  await page.locator("#addProvider").click();
  const sheet = page.locator("#addSheet");
  await sheet.locator(".tile").first().waitFor();
  await sheet.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)));
  return sheet;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": partners come first in the add sheet", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [900, 440]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, partners));
        const sheet = await openSheet(page);

        // first, under a heading that says they are sponsors
        const kinds = await sheet.locator(".kind").evaluateAll((ks) => ks.map((k) => [k.querySelector("b").textContent, k.querySelector("span")?.textContent || ""]));
        assert.deepEqual(kinds[0], w.head);
        if (w.subs) assert.equal(kinds[1][0], w.subs);

        // its rows: listed in the page's language only, with no badge, the
        // tagline on a quiet second line under the name and in the title
        // (yetone: 上 A 方案)
        const grid = sheet.locator(".kind-row + .grid");
        const rows = grid.locator(".tile");
        assert.deepEqual(await rows.locator(".n").allTextContents(), w.rows);
        const look = await rows.evaluateAll((rs) => rs.map((r) => ({ badge: r.querySelectorAll(".badge").length, over: r.scrollWidth > r.clientWidth + 1, h: r.getBoundingClientRect().height })));
        for (const l of look) {
          assert.equal(l.badge, 0);
          assert.equal(l.over, false);
          assert(l.h <= 52, "row " + l.h + "px tall");
        }
        const acme = rows.filter({ hasText: "Acme AI" });
        assert.equal(await acme.locator(".sub").textContent(), w.note);
        const [nb, sb] = await Promise.all([acme.locator(".n").boundingBox(), acme.locator(".sub").boundingBox()]);
        assert(sb.y >= nb.y + nb.height - 1, "tagline under the name");
        assert.match(await acme.getAttribute("title"), new RegExp("Acme AI · " + w.note));
        assert.match(await acme.getAttribute("title"), /api\.acme\.example\.com/);
        assert.equal(await rows.filter({ hasText: "Taken" }).locator(".have").count(), 1);
        // a sponsored built-in preset keeps its badge where it is
        assert.equal(await sheet.locator(".tile", { hasText: "OpenRouter" }).locator(".badge").count(), 1);

        // the tagline finds it
        await page.locator("#addSheet input").first().fill(w.note.toLowerCase());
        await sheet.locator(".kind").first().waitFor();
        assert.deepEqual(await sheet.locator(".tile .n").allTextContents(), ["Acme AI"]);
        await page.locator("#addSheet input").first().fill("");

        // its editor shows the tagline
        await sheet.locator(".tile", { hasText: "Acme AI" }).click();
        await page.locator("#modal .editor .ehead .note").waitFor();
        assert.equal(await page.locator("#modal .editor .ehead .note").textContent(), w.note);
        // and its site by the partner's own host, not usemagpie.ai's,
        // following the region picked
        const site = page.locator("#modal .editor .ehead .link.site");
        assert.equal(await site.textContent(), "cn.acme.example ↗");
        // China's and Global's accounts don't cross (provider.SplitAccounts):
        // the picker says to pick by the account
        assert.equal(await page.locator("#modal .editor .segs.regions").locator("xpath=following-sibling::div[contains(@class,'hint')]").textContent(), w.region);
        await page.locator("#modal .editor .segs.regions .opt").nth(1).click();
        assert.equal(await site.textContent(), "global.acme.example ↗");
        assert.equal(await site.getAttribute("data-url"), "https://usemagpie.ai/go/acme/site/global");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
    // how often each partner is shown, opened and its key page opened is
    // counted (provider.CountPartner): shown once per opening of the sheet,
    // not again as it is redrawn or searched
    await t.test("counted", async () => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
      const counted = [];
      const list = partners.map((p) => (p.id === "acme" ? { ...p, keysUrl: "https://acme.example/keys" } : p));
      await page.route("**/*", server("en", list, counted));
      const sheet = await openSheet(page);
      const input = page.locator("#addSheet input").first();
      await input.fill("acme");
      await sheet.locator(".kind").first().waitFor();
      await input.fill("");
      await sheet.locator(".tile", { hasText: "Taken" }).waitFor();
      await page.waitForTimeout(100);
      assert.deepEqual(counted, [{ what: "shown", ids: ["acme", "taken"] }]);

      await sheet.locator(".tile", { hasText: "Acme AI" }).click();
      await page.locator("#modal .editor").getByRole("button", { name: "Get a key ↗" }).click();
      await page.waitForTimeout(100);
      assert.deepEqual(counted.slice(1), [{ what: "opened", ids: ["acme"] }, { what: "keys", ids: ["acme"] }]);
      await page.context().close();
    });
    // a partner listed since the sheet last showed them puts a small dot on
    // the add button, named in its title, until the sheet shows it; one
    // added, or not listed in the page's language, doesn't
    for (const [lang, title] of [["en", "New in Partners: Acme AI"], ["zh", "合作伙伴有新成员：Acme AI, China Only"]]) for (const width of [900, 440]) {
      await t.test("new partner dot " + lang + " " + width, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const counted = [];
        const list = partners.map((p) => ({ ...p, new: true }));
        await page.route("**/*", server(lang, list, counted));
        await page.goto("http://magpie.test/?view=providers");
        const add = page.locator("#addProvider");
        await add.waitFor();
        await page.waitForFunction(() => document.querySelector("#addProvider").classList.contains("has-new"));
        assert.equal(await add.getAttribute("title"), title);
        const dot = await add.evaluate((b) => { const s = getComputedStyle(b, "::after"); const r = b.getBoundingClientRect(); return { w: s.width, h: s.height, bg: s.backgroundColor, over: b.scrollWidth > b.clientWidth + 1, right: r.right <= innerWidth }; });
        assert.deepEqual([dot.w, dot.h, dot.over, dot.right], ["6px", "6px", false, true]);
        assert.notEqual(dot.bg, "rgba(0, 0, 0, 0)");
        // Providers in the sidebar has the same news, a still dot
        const nav = page.locator('#nav button[data-view="providers"]');
        assert.equal(await nav.evaluate((b) => b.classList.contains("has-dot")), true);
        assert.equal(await nav.getAttribute("title"), title);

        // the sheet shows them: no dot, then or after the page comes back
        await add.click();
        await page.locator("#addSheet .tile").first().waitFor();
        assert.equal(await add.evaluate((b) => b.classList.contains("has-new")), false);
        assert.equal(await nav.evaluate((b) => b.classList.contains("has-dot")), false);
        assert.equal(await nav.getAttribute("title"), null);
        // each new partner's tile has its own dot, inside the tile, and one
        // listed only in another language isn't there to have one
        const tileDots = () => page.locator("#addSheet .tile").evaluateAll((ts) => ts.filter((x) => x.querySelector(".new-dot")).map((x) => {
          const d = x.querySelector(".new-dot"), s = getComputedStyle(d), r = d.getBoundingClientRect(), o = x.getBoundingClientRect();
          return { name: x.dataset.pick, w: s.width, h: s.height, bg: s.backgroundColor, inside: r.left >= o.left && r.right <= o.right && r.top >= o.top && r.bottom <= o.bottom, label: d.getAttribute("aria-label") };
        }));
        const want = lang === "en" ? ["Acme AI"] : ["Acme AI", "China Only"];
        let dots = await tileDots();
        assert.deepEqual(dots.map((d) => d.name), want);
        for (const d of dots) {
          assert.deepEqual([d.w, d.h, d.inside, d.label], ["6px", "6px", true, lang === "en" ? "New" : "新"]);
          assert.notEqual(d.bg, "rgba(0, 0, 0, 0)");
        }
        // a dot scales in and rings once, then rests
        await page.waitForFunction(() => !document.querySelector("#addSheet .new-dot.in"), null, { timeout: 8000 });
        // the dots outlast the sheet showing them, closing and opening again
        await page.keyboard.press("Escape");
        await page.locator("#addSheet").waitFor({ state: "hidden" });
        await add.click();
        await page.locator("#addSheet .tile").first().waitFor();
        assert.deepEqual((await tileDots()).map((d) => d.name), want);
        // a dot drawn again doesn't scale in again
        assert.equal(await page.locator("#addSheet .new-dot.in").count(), 0);
        // opened, Acme's dot is spent; the others keep theirs, in the sheet
        // it folds back into and in the sheet opened again
        await page.locator('#addSheet .tile[data-pick="Acme AI"]').click();
        await page.locator("#modal .editor").waitFor();
        await page.keyboard.press("Escape");
        await page.locator("#modal .editor").waitFor({ state: "detached" });
        await page.locator('#addSheet .tile[data-pick="Acme AI"]').waitFor();
        assert.deepEqual((await tileDots()).map((d) => d.name), want.slice(1));
        await page.keyboard.press("Escape");
        await page.locator("#addSheet").waitFor({ state: "hidden" });
        await add.click();
        await page.locator("#addSheet .tile").first().waitFor();
        assert.deepEqual((await tileDots()).map((d) => d.name), want.slice(1));
        await page.keyboard.press("Escape");
        await page.locator("#addSheet").waitFor({ state: "hidden" });
        assert.equal(await add.evaluate((b) => b.classList.contains("has-new")), false);
        assert.equal(await add.getAttribute("title"), null);
        await page.reload();
        await add.waitFor();
        await page.waitForTimeout(200);
        assert.equal(await add.evaluate((b) => b.classList.contains("has-new")), false);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
    // the app opened on another page reads the list in the background and
    // dots Providers in the sidebar from it: small, still, inside the row,
    // in the accent; the page opened keeps it until the sheet shows them
    // 1280 has the sidebar, 900 the header's tabs
    for (const lang of ["en", "zh"]) for (const width of [1280, 900]) await t.test(`new partner dot in the nav ${lang} ${width}`, async () => {
      const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const counted = [];
      await page.route("**/*", server(lang, partners.map((p) => ({ ...p, new: true })), counted));
      await page.goto("http://magpie.test/?view=agents");
      const nav = page.locator('#nav button[data-view="providers"]');
      await nav.waitFor();
      await page.waitForFunction(() => document.querySelector('#nav button[data-view="providers"]').classList.contains("has-dot"));
      assert.equal(await nav.getAttribute("title"), lang === "en" ? "New in Partners: Acme AI" : "合作伙伴有新成员：Acme AI, China Only");
      const dot = await nav.evaluate((b) => {
        const s = getComputedStyle(b, "::after"), r = b.getBoundingClientRect();
        return { side: document.body.classList.contains("side-nav"), w: s.width, h: s.height, bg: s.backgroundColor, anim: s.animationName, inside: parseFloat(s.right) + parseFloat(s.width) <= r.width };
      });
      const px = width === 1280 ? "6px" : "5px";
      assert.deepEqual([dot.side, dot.w, dot.h, dot.anim, dot.inside], [width === 1280, px, px, "none", true]);
      assert.notEqual(dot.bg, "rgba(0, 0, 0, 0)");
      await nav.click();
      const add = page.locator("#addProvider");
      await add.waitFor();
      assert.equal(await nav.evaluate((b) => b.classList.contains("has-dot")), true);
      await add.click();
      await page.locator("#addSheet .tile").first().waitFor();
      assert.equal(await nav.evaluate((b) => b.classList.contains("has-dot")), false);
      await page.keyboard.press("Escape");
      await page.locator("#addSheet").waitFor({ state: "hidden" });
      // read again, the list says they were shown
      await page.reload();
      await nav.waitFor();
      await page.waitForTimeout(300);
      assert.equal(await nav.evaluate((b) => b.classList.contains("has-dot")), false);
      assert.deepEqual(errors, []);
      await page.context().close();
    });
    await t.test("no partners, no heading", async () => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
      await page.route("**/*", server("en", []));
      const sheet = await openSheet(page);
      const heads = await sheet.locator(".kind b").allTextContents();
      assert(!heads.includes("Partners"), heads.join(","));
      assert.equal(heads[0], "Subscriptions");
      await page.context().close();
    });
  });
}
