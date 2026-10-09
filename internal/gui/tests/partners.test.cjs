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
  partner("acme", "Acme AI", { notes: { en: "Fast relay", zh: "快速中转", ja: "高速な中継" } }),
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
  en: { head: ["Partners", "magpie's sponsors"], subs: "Subscriptions", rows: ["Acme AI", "Taken"], note: "Fast relay" },
  zh: { head: ["合作伙伴", "赞助 magpie 的服务"], subs: "订阅", rows: ["Acme AI", "China Only", "Taken"], note: "快速中转" },
  ja: { head: ["パートナー", "magpie のスポンサー"], subs: null, rows: ["Acme AI", "Taken"], note: "高速な中継" },
  de: { head: ["Partner", "Sponsoren von magpie"], subs: null, rows: ["Acme AI", "Taken"], note: "Fast relay" },
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

        // its rows: listed in the page's language only, one quiet line each
        // with no badge, the tagline in the title
        const grid = sheet.locator(".kind").first().locator("xpath=following-sibling::div[1]");
        const rows = grid.locator(".tile");
        assert.deepEqual(await rows.locator(".n").allTextContents(), w.rows);
        const look = await rows.evaluateAll((rs) => rs.map((r) => ({ badge: r.querySelectorAll(".badge").length, over: r.scrollWidth > r.clientWidth + 1, h: r.getBoundingClientRect().height })));
        for (const l of look) {
          assert.equal(l.badge, 0);
          assert.equal(l.over, false);
          assert(l.h <= 40, "row " + l.h + "px tall");
        }
        const acme = rows.filter({ hasText: "Acme AI" });
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

        // the sheet shows them: no dot, then or after the page comes back
        await add.click();
        await page.locator("#addSheet .tile").first().waitFor();
        assert.equal(await add.evaluate((b) => b.classList.contains("has-new")), false);
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
