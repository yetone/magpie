// Run with Node's test runner and Playwright on the module path; see README.md.
// Plugins › Discover's "Have one in mind?" box (install by name) floats at
// the foot of the view while the cards scroll under it (0x3108 on X: with
// many plugins it was found only after scrolling to the very end). With
// many cards (a kind picked, shown whole) it is in sight at the top of the list and mid-way, on a strip
// that hides the cards passing under it; at the end of the list every card
// is above it and the npm registry line is below it, as before. A card
// reached with Tab is not left under it, and the search field still takes
// the keys. Every language, both engines, at 390, 560 and window widths;
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const listings = Array.from({ length: 40 }, (_, i) => ({
  package: "@magpie-community/opencode-p" + i + "-auth", name: "Plan " + i, community: true, providers: ["p" + i],
  summary: { en: "Signs in to plan " + i + " and serves its models" }, npm: { version: "0.1." + i },
}));
const state = { bun: true, bunVersion: "1.3.0", plugins: [], picker: true, registry: { url: "https://registry.npmjs.org", from: "npmrc", path: "~/.npmrc" } };

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/market") return json({ listings, state });
    if (url.pathname === "/api/plugins/github") return json({ repos: [], topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const LABEL = { en: "Have one in mind?", zh: "已有想装的？", "zh-TW": "已有想裝的？", ja: "入れたいものがありますか？", de: "Schon eines im Sinn?" };
const SIZES = [[390, 780], [560, 800], [980, 820], [1280, 800]];

const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

// where the box is against the view, and what is drawn just above and
// below it: the box (its strip), never a card
function where() {
  const view = document.querySelector("#view-plugins");
  const box = view.querySelector(".pm-manual");
  const v = view.getBoundingClientRect(), b = box.getBoundingClientRect();
  const x = b.left + 30;
  const at = (y) => {
    const e = document.elementFromPoint(x, y);
    return e ? (box.contains(e) ? "box" : e.closest(".pm-card") ? "card" : e.className || e.tagName) : "none";
  };
  return {
    top: b.top, bottom: b.bottom, vTop: v.top, vBottom: v.top + view.clientHeight,
    scrollTop: view.scrollTop, room: view.scrollHeight - view.clientHeight,
    above: at(b.top - 4), below: Math.min(b.bottom + 2, v.top + view.clientHeight - 1) > b.bottom ? at(Math.min(b.bottom + 2, v.top + view.clientHeight - 1)) : "box",
    label: box.querySelector(".pm-mlabel").textContent,
  };
}

// the reader's wheel takes the view to a share of its scroll (magpie puts
// back a scroll no reader asked for), and waits for it to stop there
async function scrollTo(page, f) {
  const v = page.locator("#view-plugins");
  const b = await v.boundingBox();
  await page.mouse.move(b.x + b.width / 2, b.y + Math.min(b.height / 2, 200));
  const near = ([top, max]) => f >= 1 ? top >= max - 1 : Math.abs(top - max * f) <= 40;
  const read = () => v.evaluate((e) => [e.scrollTop, e.scrollHeight - e.clientHeight]);
  for (let i = 0; i < 100; i++) {
    let at = await read();
    if (near(at)) {
      await page.waitForTimeout(250);
      at = await read();
      if (near(at)) return at;
    }
    await page.mouse.wheel(0, Math.max(-600, Math.min(600, Math.round(at[1] * f) - at[0] + (f >= 1 ? 50 : 0))));
    await page.waitForTimeout(60);
  }
  return read();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Discover's install box stays in sight over many plugins", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(LABEL)) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: SIZES[0][0], height: SIZES[0][1] } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".pm-manual").waitFor();
        // Discover shows a shelf of each kind; the kind picked shows it
        // whole, the many cards this is about
        await view.locator(".pm-bar .pm-kinds .opt").nth(1).click();
        await page.waitForFunction((n) => document.querySelectorAll("#view-plugins .pm-card").length >= n, listings.length);
        await view.locator(".pm-registry").waitFor();

        for (const [w, h] of SIZES) {
          const size = `${w}x${h}`;
          await page.setViewportSize({ width: w, height: h });
          await scrollTo(page, 0);
          let p = await page.evaluate(where);
          assert.ok(p.room > p.vBottom - p.vTop, `${size}: the cards run well past the view (${p.room})`);
          assert.equal(p.label, LABEL[lang]);
          for (const [when, to] of [["at the top", 0], ["mid-way", 0.5]]) {
            const [top, max] = await scrollTo(page, to);
            assert.ok(Math.abs(top - max * to) <= 40, `${size} ${when}: scrolled there (${top} of ${max})`);
            p = await page.evaluate(where);
            assert.ok(p.top >= p.vTop && p.bottom <= p.vBottom + 0.5, `${size} ${when}: the box is in the view (${JSON.stringify(p)})`);
            assert.ok(p.vBottom - p.bottom <= 12, `${size} ${when}: at the view's foot (${JSON.stringify(p)})`);
            assert.equal(p.above, "box", `${size} ${when}: no card shows just above the box`);
            assert.equal(p.below, "box", `${size} ${when}: no card shows just below the box`);
            await shot(page, `plugin-manual-float-${engine}-${lang}-${size}-${to}`);
          }

          // the end of the list: every card above the box, the registry
          // line below it, all in the view
          const [top, max] = await scrollTo(page, 1);
          assert.ok(top >= max - 1, `${size}: scrolled to the end (${top} of ${max})`);
          const end = await page.evaluate(() => {
            const v = document.querySelector("#view-plugins");
            const vb = v.getBoundingClientRect().top + v.clientHeight;
            const box = v.querySelector(".pm-manual").getBoundingClientRect();
            const cards = [...v.querySelectorAll(".pm-card")].map((c) => c.getBoundingClientRect().bottom);
            const reg = v.querySelector(".pm-registry").getBoundingClientRect();
            return { lastCard: Math.max(...cards), boxTop: box.top, boxBottom: box.bottom, regTop: reg.top, regBottom: reg.bottom, vb };
          });
          assert.ok(end.lastCard <= end.boxTop - 8, `${size}: the last card is above the box (${JSON.stringify(end)})`);
          assert.ok(end.regTop >= end.boxBottom && end.regBottom <= end.vb + 0.5, `${size}: the registry line is under the box, in sight (${JSON.stringify(end)})`);
          await shot(page, `plugin-manual-float-${engine}-${lang}-${size}-end`);
        }

        // a card reached with Tab is not left under the box
        await page.setViewportSize({ width: SIZES[2][0], height: SIZES[2][1] });
        await scrollTo(page, 0);
        const under = await page.evaluate(() => {
          const v = document.querySelector("#view-plugins");
          const top = v.querySelector(".pm-manual").getBoundingClientRect().top;
          const cards = [...v.querySelectorAll(".pm-card")];
          const i = cards.findIndex((c) => c.getBoundingClientRect().bottom > top);
          cards[i - 1].focus();
          return i;
        });
        assert.ok(under > 0, "a card is under the box at the top of the list");
        for (let k = 0; k < 8; k++) {
          await page.keyboard.press("Tab");
          await page.waitForTimeout(30);
          const f = await page.evaluate(() => {
            const a = document.activeElement, v = document.querySelector("#view-plugins");
            const card = a.closest(".pm-card");
            if (!card) return null;
            return { bottom: a.getBoundingClientRect().bottom, top: v.querySelector(".pm-manual").getBoundingClientRect().top };
          });
          if (f) assert.ok(f.bottom <= f.top + 0.5, `the focused card is above the box (${JSON.stringify(f)})`);
        }

        // the search field still takes the keys, and gives the list back
        const q = view.locator(".pm-find input");
        await q.click();
        await page.keyboard.type("Plan 3");
        assert.equal(await q.inputValue(), "Plan 3");
        assert.equal(await page.evaluate(() => document.activeElement === document.querySelector("#view-plugins .pm-find input")), true);
        await page.keyboard.press("Escape");
        await view.locator(".pm-manual").waitFor();
        assert.deepEqual(errors, []);
      });
    }
  });
}
