// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's chip said Smart for every request, and nothing on it
// changed that (xczzhh on X: "我的一直是smart，也改不了"). A provider with
// only one account or key on has nothing to route: its chip and the
// Accounts and keys heading say no routing, and the mode paragraph says
// how to get one. Where a provider routes over several, the chip and the
// heading are a way to its routing under Several accounts or keys: a
// click, or Enter, brings that row into view, marks it and focuses its
// routing — the page moves only because the reader asked. In every
// language, both engines, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const key = (provider, who) => ({ id: provider + "#" + who, provider, name: provider === "relay" ? "Relay" : "Solo", who, kind: "key", model: "gpt-6-astra", routing: "" });

const scenes = {
  lone: { order: [key("solo", "solo-a")], pools: [] },
  pooled: {
    order: [key("relay", "relay-a"), key("relay", "relay-b")],
    pools: [{ provider: "relay", name: "Relay", icon: "generic", kind: "key", who: ["relay-a", "relay-b"], routing: "", affinity: "" }],
  },
};
// filler groups above the pools, so that the way to them is a long one
const filler = Array.from({ length: 14 }, (_, i) => ({ id: "g" + i, name: "Filler " + i, routing: "", members: ["relay/gpt-6-astra"],
  ready: true, memberInfo: [{ id: "relay/gpt-6-astra", ready: true }], offers: [], shared: [] }));

function serve(lang, scene) {
  const s = scenes[scene];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "gpt-6-astra", provider: s.order[0].provider, done: true, status: 200, ms: 900, tokens: 1200,
    order: s.order, tries: [{ id: s.order[0].id, model: "gpt-6-astra", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [{ id: "relay/gpt-6-astra", name: "gpt-6-astra", providerName: "Relay", icon: "generic" }], groups: filler, pools: s.pools });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words, as each language says them
const lone = {
  en: "Only one account or key is on for this, so routing has nothing to choose between. Turn on a second in the provider's editor, and how it routes is set under Several accounts or keys below.",
  zh: "这里只开了一个账号或 Key，路由没有可选的。在供应商的编辑页再开一个，路由方式就在下面的「多账号 / 多 Key」里设置。",
  "zh-TW": "這裡只開了一個帳號或 Key，路由沒有可選的。在供應商的編輯頁再開一個，路由方式就在下面的「多帳號 / 多 Key」裡設定。",
  ja: "有効なアカウントやキーが 1 つだけなので、ルーティングに選択肢はありません。プロバイダの編集画面で 2 つ目を有効にすると、ルーティングは下の「複数のアカウントやキー」で設定できます。",
  de: "Hier ist nur ein Konto oder Schlüssel aktiv, also hat die Weiterleitung keine Wahl. Aktiviere im Editor des Anbieters ein zweites; wie weitergeleitet wird, legst du dann unten unter „Mehrere Konten oder Schlüssel“ fest.",
};
const change = { en: "Change how Relay routes", zh: "改 Relay 的路由方式", "zh-TW": "改 Relay 的路由方式", ja: "Relay のルーティングを変更", de: "Ändern, wie Relay weiterleitet" };
const smart = { en: "Smart", zh: "智能", "zh-TW": "智慧", ja: "スマート", de: "Intelligent" };

async function open(engine, lang, scene, width, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  // counts the routing view's once-a-second redraws, to hold a press over one
  await page.addInitScript(() => {
    const every = window.setInterval;
    window.__redraws = 0;
    window.setInterval = (f, ms, ...a) => every(typeof f === "function" && String(f).includes("renderActs") ? (...x) => { f(...x); window.__redraws++; } : f, ms, ...a);
  });
  await page.route("**/*", serve(lang, scene));
  await page.goto("http://magpie.test/?view=routing");
  const req = page.locator(".rt-req").first();
  await req.waitFor();
  if (await req.getAttribute("aria-pressed") !== "true") await req.click();
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: one key on has no routing to name, and says how to get one`, async (t) => {
        const { page, errors } = await open(engine, lang, "lone", width, t);
        await page.waitForFunction((s) => document.querySelector(".rt-mode")?.textContent.trim() === s, lone[lang]);
        // the groups are in, and the chip still names no routing
        await page.locator("#view-groups .rt-group").first().waitFor({ state: "attached" });
        await page.waitForTimeout(150);
        assert.equal(await page.locator(".rt-hub i").isHidden(), true, "the chip names no routing");
        assert.equal((await page.locator(".rt-mode").textContent()).trim(), lone[lang]);
        await page.locator(".rt-prov").first().waitFor();
        assert.equal((await page.locator(".rt-prov").first().innerText()).trim(), "Solo", "the heading names no routing");
        assert.equal(await page.locator(".rt-go").count(), 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a provider routing over several keys is a way to where it is set`, async (t) => {
        const { page, errors } = await open(engine, lang, "pooled", width, t);
        const chip = page.locator(".rt-hub i.rt-go");
        await chip.waitFor();
        assert.equal((await chip.textContent()).trim(), smart[lang]);
        assert.equal(await chip.getAttribute("title"), change[lang]);
        assert.equal(await chip.getAttribute("role"), "button");
        assert.notEqual((await page.locator(".rt-mode").textContent()).trim(), lone[lang]);
        // the heading names it too, the same way to it
        const head = page.locator(".rt-prov > span.rt-go");
        await head.waitFor();
        assert.equal((await head.textContent()).trim(), smart[lang]);
        assert.equal(await head.getAttribute("data-go"), "pool:relay");

        // the pools are on the Routing groups page: the chip opens it, with
        // the pool in sight
        const groups = page.locator("#view-groups");
        const pool = page.locator('.rt-pool[data-provider="relay"]');
        await pool.waitFor({ state: "attached" });
        const inView = () => pool.evaluate((p) => { const v = document.querySelector("#view-groups"), b = v.getBoundingClientRect(), r = p.getBoundingClientRect();
          return !v.hidden && r.height > 0 && r.top >= b.top && r.bottom <= b.bottom; });
        assert.equal(await inView(), false, "the pool starts out of view");
        const back = async () => {
          await page.evaluate(() => window.show("routing"));
          await page.waitForFunction(() => !document.querySelector("#view-routing").hidden);
          await pool.evaluate((p) => p.classList.remove("rt-found"));
        };

        // a click takes the reader there: in view, marked, its routing focused
        await chip.click();
        await page.waitForFunction(() => document.querySelector('.rt-pool[data-provider="relay"]')?.classList.contains("rt-found"));
        assert.equal(await groups.isVisible(), true, "the groups page opened");
        assert.equal(await inView(), true);
        assert.equal(await page.evaluate(() => document.activeElement?.closest(".rt-pool")?.dataset.provider), "relay");
        assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("on")), true);
        assert.equal((await page.evaluate(() => document.activeElement.textContent)).trim(), smart[lang]);

        // a press held over the head's redraw is still a click (#2151): the
        // chip is not written over under the pointer, which WebKit takes
        // for a press that never ends where it began
        await back();
        const box = await chip.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        const drawn = await page.evaluate(() => window.__redraws);
        await page.waitForFunction((n) => window.__redraws > n, drawn, { polling: 50 });
        await page.mouse.up();
        await page.waitForFunction(() => document.querySelector('.rt-pool[data-provider="relay"]')?.classList.contains("rt-found"));
        assert.equal(await inView(), true);

        // the keyboard does the same
        await back();
        await chip.focus();
        await page.keyboard.press("Enter");
        await page.waitForFunction(() => document.querySelector('.rt-pool[data-provider="relay"]')?.classList.contains("rt-found"));
        assert.equal(await inView(), true);
        // nothing is drawn wider than the window
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        assert.deepEqual(errors, []);
      });
    }
  }
}
