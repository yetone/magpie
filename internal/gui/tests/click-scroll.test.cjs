// Run with Node's test runner and Playwright on the module path; see README.md.
// A click never moves the page: what was clicked stays where it is on the
// screen, and code scrolls a view only with the reader's click in hand.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const who = { id: "fixture-key", provider: "fixture", name: "Fixture", who: "key-1", kind: "key", model: "model-a", routing: "", used: 0 };
const routes = Array.from({ length: 12 }, (_, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "fixture", model: "model-a", provider: "fixture",
  order: [who], tries: [{ id: who.id, model: "model-a", start: at(i), done: true, status: 200, ms: 1200 }],
  done: true, status: 200, ms: 1200, tokens: 3000,
}));
const state = { agents: [{ id: "fixture", name: "Fixture", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } };

async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json(state);
  if (url.pathname === "/api/gateway/trace") {
    // live keeps none: the gateway started a moment ago; the day has them
    if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
    return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
  }
  if (url.pathname === "/api/gateway/history") {
    const d = url.searchParams.get("day");
    return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
  }
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

const view = "#view-routing";
const top = (page, sel) => page.locator(sel).evaluate((e) => e.getBoundingClientRect().top);
const settle = (page) => page.waitForTimeout(900);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a click never moves the page", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 1100, height: 640 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, engine + "-click.png") });
      }
      await browser.close();
    });
    async function reset() {
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor();
    }
    // the reader scrolls the view till sel is y under its top, or less
    async function scrollTo(sel, y = 300) {
      const box = await page.locator(view).boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + 40);
      for (let i = 0, last; i < 150 && (await top(page, sel)) - box.y > y; i++) {
        const now = await page.locator(view).evaluate((v) => v.scrollTop);
        if (now === last) break; // at the end
        last = now;
        await page.mouse.wheel(0, 20);
        await page.waitForTimeout(20);
      }
      await page.waitForTimeout(300);
      assert((await top(page, sel)) > box.y, "what's clicked must be in sight, or the click itself scrolls to it");
      assert(await page.locator(view).evaluate((v) => v.scrollTop > 0), "the view must have scrolled");
    }

    await t.test("Live and a day, picked in turn, stay under the pointer", async () => {
      await reset();
      await page.locator(".rt-day").nth(1).click(); // the day's requests, the list full
      await settle(page);
      // down to the list's end: Live, with none, leaves the page shorter
      await scrollTo(".rt-days", 0);
      assert(await page.locator(view).evaluate((v) => v.scrollTop + v.clientHeight >= v.scrollHeight - 2), "the view must be at its end");
      for (const i of [0, 1, 0, 1]) {
        const chip = page.locator(".rt-day").nth(i), was = await top(page, ".rt-days");
        await chip.click();
        await settle(page);
        assert.equal(await page.locator(".rt-day").nth(i).getAttribute("aria-pressed"), "true");
        const is = await top(page, ".rt-days");
        assert(Math.abs(is - was) <= 1, `picking ${i ? "the day" : "Live"} moved the page ${Math.round(is - was)}px`);
      }
    });

    await t.test("code can't take the page from a click", async () => {
      await reset();
      await page.locator(view).evaluate((v) => {
        const s = document.createElement("div");
        s.style.cssText = "flex:none;height:1600px";
        const mk = (id, fn) => { const b = document.createElement("button"); b.id = id; b.textContent = id; b.style.flex = "none"; b.onclick = fn; return b; };
        const above = document.createElement("div");
        above.style.flex = "none";
        v.prepend(above);
        v.append(s,
          // sets scrollTop outright
          mk("jump", () => { v.scrollTop = 0; }),
          // the page above it grows when a load comes in
          mk("grow", () => setTimeout(() => { above.style.height = "400px"; }, 300)),
          // redraws itself, and everything after it
          mk("redraw", (e) => { const b = e.currentTarget, c = b.cloneNode(true); c.onclick = b.onclick; above.style.height = "120px"; b.replaceWith(c); }),
          // says it's on purpose, but with no click in hand
          mk("sneak", () => setTimeout(() => { scrollOnPurpose(); v.scrollTop = 0; }, 50)),
          // scrollIntoView from a helper a click only shares
          mk("helper", () => setTimeout(() => document.querySelector("#rt").scrollIntoView(), 50)),
          document.createElement("div"));
        v.lastChild.style.cssText = "flex:none;height:600px";
      });
      for (const id of ["jump", "grow", "redraw", "sneak", "helper"]) {
        await scrollTo("#" + id);
        const was = await top(page, "#" + id);
        await page.locator("#" + id).click();
        await settle(page);
        const is = await top(page, "#" + id);
        assert(Math.abs(is - was) <= 1, `${id} moved the page ${Math.round(is - was)}px`);
      }
    });

    await t.test("a click that asks to go somewhere does", async () => {
      await reset();
      await page.locator(view).evaluate((v) => {
        const s = document.createElement("div");
        s.style.cssText = "flex:none;height:1600px";
        const b = document.createElement("button");
        b.id = "go"; b.textContent = "go"; b.style.flex = "none";
        b.onclick = (e) => { if (scrollOnPurpose(e)) v.scrollTop = 0; };
        v.append(s, b);
      });
      await scrollTo("#go");
      await page.locator("#go").click();
      await settle(page);
      assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), 0);
    });

    await t.test("the room kept for a click goes as the reader scrolls back", async () => {
      await reset();
      await page.locator(".rt-day").nth(1).click();
      await settle(page);
      await scrollTo(".rt-days", 0);
      await page.locator(".rt-day").nth(0).click(); // Live: none, the page shorter
      await settle(page);
      assert(await page.locator(view).evaluate((v) => !!v.querySelector(":scope > .view-room")), "room must be kept");
      const box = await page.locator(view).boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + 60);
      for (let i = 0; i < 30; i++) { await page.mouse.wheel(0, -40); await page.waitForTimeout(20); }
      await page.waitForTimeout(300);
      assert.equal(await page.locator(view).evaluate((v) => v.querySelector(":scope > .view-room")), null, "the room must go");
    });

    await t.test("the reader's wheel still scrolls after a click", async () => {
      await reset();
      await page.locator(view).evaluate((v) => { const s = document.createElement("div"); s.style.cssText = "flex:none;height:1600px"; v.append(s); });
      await page.locator(".rt-day").nth(1).click();
      const was = await page.locator(view).evaluate((v) => v.scrollTop);
      const box = await page.locator(view).boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + 60);
      await page.mouse.wheel(0, 300);
      await page.waitForTimeout(400);
      assert((await page.locator(view).evaluate((v) => v.scrollTop)) > was + 100, "the wheel must scroll the view");
    });

    assert.deepEqual(errors, [], "page runtime errors");
  });
}
