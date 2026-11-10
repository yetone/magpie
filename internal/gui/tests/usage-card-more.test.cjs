// Run with Node's test runner and Playwright on the module path; see README.md.
// A Usage card has a ⋯ at its head's end that opens the card's menu
// (jen_celeus on X: OpenCode Zen's card, with no usage information, wanted
// hiding, and the right-click that hid it was nowhere to be seen). On a
// card with nothing but its name and plan, Hide from Overview is one click
// on ⋯ away; the line under the cards has it back. Where a pointer hovers,
// the ⋯ shows as the card is hovered and takes no room, so the head is laid
// out as before; where nothing hovers it is always shown and nothing in the
// head runs under it. No click moves the page. Every language, a wide
// window, a narrow one and magpie web on a phone; no backend, the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { isDeepStrictEqual } = require("node:util");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const QUOTAS = [
  { provider: "claude", name: "Claude", icon: "claude-color", plan: "max", windows: [{ name: "5h", used: 70, resetSecs: 3 * 3600 }] },
  // what the zen-free plugin's usage() answers: a plan, no windows
  { provider: "opencode-zen-free", name: "OpenCode Zen", icon: "opencode", plan: "OpenCode Zen Free", windows: [] },
];

function server(lang, web, saved, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const settings = () => ({ lang, theme: "light", usageCardsHidden: saved.hidden });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [], profiles: [], settings: settings(), fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/arrange") {
      const body = JSON.parse(req.postData() || "{}");
      posts.push(body);
      if (body.cardsHidden) saved.hidden = body.cardsHidden;
      return json(settings());
    }
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/usage/quotas") return json(QUOTAS);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, cache_read: 0, cache_write: 0, unpriced: 0, cost: 0, bucket: "day", series: [], path: "~/.config/magpie/usage.jsonl", agents: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const L = {
  en: { hide: "Hide from Overview", n: (n) => `${n} hidden`, all: "Show all", label: "Actions for OpenCode Zen" },
  zh: { hide: "从概览中隐藏", n: (n) => `已隐藏 ${n} 个`, all: "全部显示", label: "OpenCode Zen 的操作" },
  "zh-TW": { hide: "從概覽中隱藏", n: (n) => `已隱藏 ${n} 個`, all: "全部顯示" },
  ja: { hide: "概要から隠す", n: (n) => `${n} 件非表示`, all: "すべて表示" },
  de: { hide: "In der Übersicht ausblenden", n: (n) => `${n} ausgeblendet`, all: "Alle anzeigen" },
};
const SIZES = [{ width: 1200, web: false }, { width: 560, web: false }, { width: 390, web: true }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a card's ⋯ hides it from the Overview", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const { width, web } of SIZES) {
        const w = L[lang];
        await t.test(`${lang} ${width}${web ? " web" : ""}`, async () => {
          const errors = [];
          const saved = { hidden: [] }, posts = [];
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce", hasTouch: web, isMobile: web && engine === "chromium" });
          t.after(() => ctx.close());
          const p = await ctx.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang, web, saved, posts));
          await p.goto("http://magpie.test/?view=usage");
          await p.locator("#subscriptionUsage .subscription-card").first().waitFor({ timeout: 20000 });
          const cards = () => p.locator("#subscriptionUsage > .subscription-card").evaluateAll((cs) => cs.map((c) => c.dataset.provider));
          const zen = p.locator('#subscriptionUsage > .subscription-card[data-provider="opencode-zen-free"]');
          const more = zen.locator(".subscription-head .us-more");
          const top = () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
          const savedAs = async (want) => {
            for (const end = Date.now() + 5000; !isDeepStrictEqual(saved.hidden, want) && Date.now() < end;) await new Promise((ok) => setTimeout(ok, 20));
            assert.deepEqual(saved.hidden, want);
          };
          assert.deepEqual(await cards(), ["claude", "opencode-zen-free"]);
          assert.equal(await more.count(), 1, "the card has a ⋯ of its own");
          if (w.label) assert.equal(await more.getAttribute("aria-label"), w.label);
          const hovers = await p.evaluate(() => matchMedia("(hover: hover)").matches);
          await p.mouse.move(1, 1);
          // where a pointer hovers: unseen at rest, shown with the card hovered;
          // where nothing hovers: always shown
          const shown = () => more.evaluate((b) => +getComputedStyle(b).opacity);
          if (hovers) {
            assert.equal(await shown(), 0, "at rest the ⋯ isn't shown");
            await zen.hover();
          }
          await more.evaluate((b) => new Promise((ok) => { const f = () => +getComputedStyle(b).opacity === 1 ? ok() : requestAnimationFrame(f); f(); }));
          // the ⋯ sits inside the card, at the head's end, and nothing in
          // the head runs under it where it is always shown
          const geo = await zen.evaluate((c) => {
            const h = c.querySelector(".subscription-head"), b = h.querySelector(".us-more").getBoundingClientRect(), cb = c.getBoundingClientRect();
            const under = [...h.children].filter((x) => x.tagName !== "BUTTON" || !x.classList.contains("us-more")).map((x) => x.getBoundingClientRect()).filter((r) => r.right > b.left + 0.5 && r.width > 0);
            return { inside: b.left >= cb.left && b.right <= cb.right && b.top >= cb.top && b.bottom <= cb.bottom, under: under.length, b: [b.left, b.top, b.right, b.bottom], cb: [cb.left, cb.top, cb.right, cb.bottom] };
          });
          assert(geo.inside, "the ⋯ is inside its card: " + JSON.stringify(geo));
          if (!hovers) assert.equal(geo.under, 0, "nothing in the head runs under the ⋯");

          await reader.inView(p, zen);
          const at = await top();
          await more.click();
          await p.locator(".row-menu").waitFor();
          assert.deepEqual(await p.locator(".row-menu .rm-item").allTextContents(), [w.hide]);
          // a second click on ⋯ closes the menu, a third opens it again
          await more.click();
          await p.locator(".row-menu").waitFor({ state: "detached" });
          await more.click();
          await p.locator(".row-menu .rm-item", { hasText: w.hide }).click();
          await p.waitForFunction(() => !document.querySelector('#subscriptionUsage > [data-provider="opencode-zen-free"]'));
          await savedAs(["opencode-zen-free|plan"]);
          assert.deepEqual(posts.at(-1), { cardsHidden: ["opencode-zen-free|plan"] });
          assert.deepEqual(await cards(), ["claude"]);
          assert.equal((await p.locator("#quotaHid .us-hidline").textContent()).trim(), w.n(1));
          assert.equal(await top(), at, "hiding the card doesn't move the page");

          // the line has it back
          await reader.click(p, p.locator("#quotaHid .us-hidline"));
          await reader.click(p, p.locator("#quotaHid .us-unhide"));
          await p.waitForFunction(() => !!document.querySelector('#subscriptionUsage > [data-provider="opencode-zen-free"]'));
          await savedAs([]);
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
