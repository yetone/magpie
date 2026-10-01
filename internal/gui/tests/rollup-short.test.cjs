// Run with Node's test runner and Playwright on the module path; see README.md.
// "Show less" under a list that, rolled up, is shorter than the window
// (#355): 24 agents, 5 in use and 19 folded (4 hidden by hand), in a tall
// window. "Show {n} more" takes the button to the view's top; "Show less"
// then leaves it there, room kept at the view's foot for it, on every frame
// that is painted. The room was worked out from scrollHeight, which is never
// less than the view is tall, so it came out short by the empty foot under
// the list, and the hold and the fit of the room took turns: the list
// swung between the top of the view and the button for the four seconds a
// click is held. With the system's Reduce motion (the fold shuts at once)
// and without. Chromium and WebKit, English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = (id, used) => ({
  id, name: id, path: "/test/" + id, fields: [
    { key: "model", label: "model", value: used ? "model-a" : "", options: models },
    { key: "effort", label: "effort", value: used ? "high" : "", options: ["low", "medium", "high"].map((v) => ({ value: v, label: v })) },
  ],
});

function serve(lang) {
  const hidden = ["fold-0", "fold-1", "fold-2", "fold-3"];
  const state = {
    agents: [...Array.from({ length: 5 }, (_, i) => agent("used-" + i, true)), ...Array.from({ length: 19 }, (_, i) => agent("fold-" + i, i < 4))],
    profiles: [], settings: { lang, theme: "light", agentsHidden: hidden },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { more: "Show 19 more (4 hidden)", less: "Show less" },
  zh: { more: "显示其余 19 个（含 4 个已隐藏）", less: "收起" },
};

// where the button is on each frame as it was painted: read after the
// frame (a task queued from its animation frame), when the page has held
// what was clicked again
async function watch(page) {
  await page.evaluate(() => {
    const v = document.querySelector("#view-agents");
    window.seen = [];
    window.watching = true;
    const frame = () => {
      if (!window.watching) return;
      setTimeout(() => {
        const m = document.querySelector(".agent-more");
        window.seen.push({ top: m.getBoundingClientRect().top - v.getBoundingClientRect().top, scroll: v.scrollTop });
      }, 0);
      requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
  });
}
async function seen(page) {
  return page.evaluate(() => { window.watching = false; return window.seen; });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Show less under a list shorter than the window keeps the button still", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const motion of ["reduce", "no-preference"]) {
        await t.test(`${lang}, reduced motion: ${motion}`, async () => {
          const page = await (await browser.newContext({ viewport: { width: 1240, height: 800 }, reducedMotion: motion })).newPage();
          page.setDefaultTimeout(5000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/");
          const more = page.locator(".agent-more");
          await more.waitFor();
          await page.waitForTimeout(400);
          assert.equal((await more.textContent()).trim(), words[lang].more);
          const view = page.locator("#view-agents");
          const short = await view.evaluate((v) => v.scrollHeight <= v.clientHeight + 1);
          assert(short, "rolled up, the list must be shorter than the view");

          // Show more: the button goes to the view's top
          await more.click();
          await page.waitForTimeout(1200);
          assert.equal((await more.textContent()).trim(), words[lang].less);
          const top = await more.evaluate((m) => m.getBoundingClientRect().top - document.querySelector("#view-agents").getBoundingClientRect().top);
          assert(top < 60 && await view.evaluate((v) => v.scrollTop) > 100, `Show more must take the button up the view (at ${Math.round(top)})`);

          // Show less: the button stays where it is, on every frame painted
          await watch(page);
          await more.click();
          await page.waitForTimeout(1500);
          const frames = await seen(page);
          assert.equal(await more.getAttribute("aria-expanded"), "false");
          assert(frames.length > 10, "the frames must be watched");
          const tops = frames.map((f) => Math.round(f.top));
          const worst = Math.max(...frames.map((f) => Math.abs(f.top - top)));
          assert(worst <= 2, `Show less moved the button ${Math.round(worst)}px (${tops.join(" ")})`);
          const now = await more.evaluate((m) => m.getBoundingClientRect().top - document.querySelector("#view-agents").getBoundingClientRect().top);
          assert(Math.abs(now - top) <= 1, `Show less left the button ${Math.round(now - top)}px from where it was`);

          // the room goes as the reader scrolls back to the top
          const box = await view.boundingBox();
          await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
          for (let i = 0; i < 20; i++) { await page.mouse.wheel(0, -60); await page.waitForTimeout(20); }
          await page.waitForTimeout(400);
          assert.equal(await view.evaluate((v) => v.scrollTop), 0);
          assert.equal(await page.locator("#view-agents > .view-room").count(), 0, "the room must go once the reader is back at the top");
          await page.context().close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
