// Run with Node's test runner and Playwright on the module path; see README.md.
// FangChangBDG on X (v0.1.3738, Windows tray window): a DeepSeek card that
// can't be read says "Allowance unavailable — hover for why". Hovering it
// showed the real error; after one press of the card's refresh, hovering
// showed only the short words again. The short words are the text of the
// line, which the page's "text cut short says its whole self" hover gave a
// title of its own over the card's, the error. The line now carries the
// reason itself, so that hover leaves it alone. Checked on the tray card and
// the Usage page's row, before and after a refresh, in every language,
// Chromium and WebKit, at the tray's widths. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const WHY = "Get \"https://api.deepseek.com/user/balance\": dial tcp: lookup api.deepseek.com: no such host";

function serve(lang, posts) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const quotas = () => [{ provider: "deepseek", name: "DeepSeek", icon: "deepseek", user: "main", error: WHY, windows: [] }];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage/quotas/refresh") { posts.push(url.search); return json(quotas()); }
    if (url.pathname === "/api/usage") return json({ calls: 0, input: 0, output: 0, cost: 0, agents: [], accounts: [], models: [], series: [], days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the tooltip the pointer would get over the line: the title of the nearest
// element that has one, after the page's own hover has run
async function tipOver(page, locator) {
  // the card is drawn again after a refresh: wait out the redraw
  let box;
  for (let i = 0; !box?.width && i < 50; i++) {
    box = await locator.boundingBox().catch(() => null);
    if (!box?.width) await page.waitForTimeout(40);
  }
  await page.mouse.move(2, 2);
  await page.mouse.move(box.x + Math.min(box.width / 2, 40), box.y + box.height / 2, { steps: 3 });
  return locator.evaluate((x) => {
    x.scrollIntoView({ block: "nearest" });
    const r = x.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + Math.min(r.width / 2, 40), r.top + r.height / 2);
    return (hit?.closest("[title]")?.getAttribute("title")) || "";
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [486, 360]) {
      test(`${engine} ${lang} ${width}px: a card that can't be read keeps its reason in the tooltip after a refresh`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 720 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?mode=panel");
        await page.locator('#ptabs [data-ptab="usage"]').click();
        const card = page.locator('.pq-card[data-card="deepseek|main"]');
        const say = card.locator(".pq-say");
        await say.waitFor();
        for (const round of ["before", "after"]) {
          if (round === "after") {
            await card.hover();
            await card.locator(".quota-refresh").click();
            await page.waitForFunction(() => !document.querySelector(".pq-card .quota-refresh.busy"));
            assert.deepEqual(posts, ["?provider=deepseek&user=main"]);
          }
          const tip = await tipOver(page, say);
          assert.ok(tip.includes(WHY), `${round} a refresh, the line's tooltip holds the reason: ${JSON.stringify(tip)}`);
          // and the line itself: the page's own hover must not have put
          // the short words over it
          const own = await say.evaluate((x) => x.closest(".pq-sub").getAttribute("title") || x.getAttribute("title") || "");
          assert.ok(own === "" || own.includes(WHY), `${round}: no short-words title on the line: ${JSON.stringify(own)}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
