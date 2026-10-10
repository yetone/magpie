// Run with Node's test runner and Playwright on the module path; see README.md.
// Expanding and collapsing on the Agents page with the list scrolled — the
// tray panel's "Show {n} more" and "Show less" with a row open, a row opened
// and closed, and the window's fold: what was clicked stays under the
// pointer, frame by frame, while the rest unroll or roll up around it — no
// slide away and snap back, no swinging between the two, nothing pushed out
// of the view.
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
const state = {
  agents: [...Array.from({ length: 10 }, (_, i) => agent("agent-" + i, true)), agent("codex", true), agent("unset-1", false), agent("unset-2", false)],
  profiles: [], settings: { lang: "en", theme: "light" },
};

async function serve(route) {
  const url = new URL(route.request().url());
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return route.fulfill({ json: state });
  if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
  if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
  if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
  if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
  if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

// clicks sel and follows watch (sel itself by default) as each frame is
// painted: laid out, held again by the page, then measured by an observer
// made after the page's own. It must not move, nor end up anywhere else.
async function stays(page, what, sel, watch = sel) {
  await page.evaluate((watch) => {
    window.was = document.querySelector(watch).getBoundingClientRect().top;
    window.tops = [];
    window.ro?.disconnect();
    window.ro = new ResizeObserver(() => window.tops.push(document.querySelector(watch).getBoundingClientRect().top));
    window.ro.observe(document.querySelector("#agents"));
  }, watch);
  await page.locator(sel).click();
  await page.waitForTimeout(1200);
  const { was, tops } = await page.evaluate(() => ({ was: window.was, tops: window.tops }));
  assert(tops.length > 5, what + ": the list must change over frames");
  const worst = Math.max(...tops.map((y) => Math.abs(y - was)));
  assert(worst <= 2, `${what} moved what was clicked ${Math.round(worst)}px as it played (${tops.map(Math.round).join(" ")})`);
  const now = await page.locator(watch).evaluate((m) => m.getBoundingClientRect().top);
  assert(Math.abs(now - was) <= 1, `${what} left what was clicked ${Math.round(now - was)}px from where it was`);
}

// "Show {n} more" clicked at the foot of a view with no room below: the rest
// unroll under the button, the rows above it staying with it, and the view
// goes down with them, never back up, till they
// are in sight or the button is at the view's top.
async function unrolls(page, what) {
  await page.evaluate(() => {
    const more = document.querySelector(".agent-more"), above = (more.closest(".ag-manage-bar") || more).previousElementSibling;
    window.frames = [];
    window.ro?.disconnect();
    window.ro = new ResizeObserver(() => {
      const m = more.getBoundingClientRect(), f = document.querySelector(".agent-fold").getBoundingClientRect();
      window.frames.push({ top: m.top, gap: m.top - above.getBoundingClientRect().top, below: f.top - m.bottom });
    });
    window.ro.observe(document.querySelector("#agents"));
  });
  const gap = await page.locator(".agent-more").evaluate((m) => m.getBoundingClientRect().top - (m.closest(".ag-manage-bar") || m).previousElementSibling.getBoundingClientRect().top);
  await page.locator(".agent-more").click();
  await page.waitForTimeout(1200);
  const frames = await page.evaluate(() => window.frames);
  assert(frames.length > 5, what + ": the list must change over frames");
  const tops = frames.map((f) => Math.round(f.top)).join(" ");
  for (const [i, f] of frames.entries()) {
    assert(Math.abs(f.gap - gap) <= 1, `${what}: the row above the button moved ${Math.round(f.gap - gap)}px from it`);
    assert(f.below >= -1, `${what}: the rest must unroll under the button, not above it`);
    if (i) assert(f.top <= frames[i - 1].top + 1, `${what}: the view went back up as the rest unrolled (${tops})`);
  }
  const end = await page.evaluate(() => {
    const v = document.querySelector("#view-agents").getBoundingClientRect();
    return { fold: document.querySelector(".agent-fold").getBoundingClientRect().bottom - v.bottom, button: document.querySelector(".agent-more").getBoundingClientRect().top - v.top };
  });
  assert(end.fold <= 1 || end.button <= 6, `${what}: what unrolled must come into sight (${JSON.stringify(end)})`);
  assert(end.button >= 0, `${what}: the button must stay in sight`);
}

// the reader scrolls the agents to their end
async function toEnd(page) {
  const view = page.locator("#view-agents");
  const box = await view.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + 60);
  for (let i = 0; i < 30; i++) { await page.mouse.wheel(0, 40); await page.waitForTimeout(20); }
  await page.waitForTimeout(400);
  assert(await view.evaluate((v) => v.scrollTop > 0 && v.scrollTop + v.clientHeight >= v.scrollHeight - 2), "the list must be scrolled to its end");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": expanding and collapsing agents keeps what was clicked in place", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-agents-fold-${i}.png`) });
      }
      await browser.close();
    });
    // with motion: the yank is in the unrolling
    const open = async (url, viewport) => {
      const page = await (await browser.newContext({ viewport })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve);
      await page.goto(url);
      await page.locator(".agent-more").waitFor();
      return page;
    };

    await t.test("the tray panel, at its tallest", async () => {
      const page = await open("http://magpie.test/?mode=panel", { width: 440, height: 560 });
      await page.locator('.row.agent[data-id="codex"] .ag-sum').click();
      await page.waitForTimeout(900);
      await toEnd(page);
      await unrolls(page, "Show more");
      assert.equal(await page.locator(".agent-more").getAttribute("aria-expanded"), "true");
      await toEnd(page);
      await stays(page, "Show less", ".agent-more");
      assert.equal(await page.locator(".agent-more").getAttribute("aria-expanded"), "false");
      // a row opened above the open one, which closes, then closed again
      await toEnd(page);
      const row = '.row.agent[data-id="agent-9"]';
      await stays(page, "opening a row", row + " .ag-sum", row);
      assert.equal(await page.locator(row).getAttribute("aria-expanded"), "true");
      await stays(page, "closing a row", row + " .ag-sum", row);
      assert.equal(await page.locator(row).getAttribute("aria-expanded"), "false");
    });

    await t.test("the window", async () => {
      const page = await open("http://magpie.test/", { width: 900, height: 480 });
      await toEnd(page);
      await unrolls(page, "Show more");
      await toEnd(page);
      await stays(page, "Show less", ".agent-more");
    });
    assert.deepEqual(errors, []);
  });
}
