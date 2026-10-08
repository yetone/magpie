// Run with Node's test runner and Playwright on the module path; see README.md.
// The Text size setting (Settings → Preferences, Ctrl/Cmd + − 0): a pick
// posts to /api/settings/text-size and stays picked after a reload (boot.js
// carries it); the shortcuts step through 100/110/125/150% and back, in the
// window and the tray panel, and a click on the control never scrolls the
// page. The zoom itself is the webview's (Go), so here it is as a browser
// zoomed: the window's points over the zoom in CSS pixels, deviceScaleFactor
// the zoom — and at 150% nothing in any view runs off to the side, the Mac
// header still level with the traffic lights. English and Chinese; no
// backend, the API is faked here. ARTIFACT_DIR gets screenshots at 100% and
// 150%, light and dark.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const usage = {
  calls: 5, errors: 0, input: 12000, output: 4000, cache_read: 500, cache_write: 100, reasoning: 200,
  unpriced: 0, cost: 12.34, bucket: "day",
  series: [
    { label: "Mon", input: 6000, output: 2000, calls: 3, cost: 7 },
    { label: "Tue", input: 6000, output: 2000, calls: 2, cost: 5.34 },
  ],
  agents: [{ name: "Claude Code", calls: 5, cost: 12.34 }],
  models: [{ name: "claude-opus", calls: 5, cost: 12.34 }],
};

const calls = Array.from({ length: 5 }, (_, i) => ({
  time: new Date(Date.now() - (i + 1) * 60e3).toISOString(), agent: "fixture", model: "fixture/model-a", status: 200, ms: 900,
}));
const providers = {
  providers: [],
  presets: [], excluded: [],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls, groups: [] },
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// one fake magpie per test: its settings live across reloads, as the real
// one's do, and every text size posted is kept in posts
function server(lang, theme, size, posts) {
  let cur = settingsPayload({ lang, theme, textSize: size });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false,textSize:${cur.textSize}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme, textSize: cur.textSize }, fx: cur.fx });
    if (url.pathname === "/api/settings/text-size") {
      const { size: n } = req.postDataJSON();
      posts.push(n);
      cur = { ...cur, textSize: n };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      // the Settings page's own save never carries the text size
      if (req.method() === "POST") cur = { ...cur, ...req.postDataJSON(), textSize: cur.textSize };
      return json(cur);
    }
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/library") return json({ dir: "~/.config/magpie/library", backups: "", home: "~", agents: [], instructions: { agents: [], sets: [] }, servers: [], foundServers: [], skills: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const zoomVar = (page) => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--zoom").trim());
const settingsTop = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);
// what runs off to the side: the page, the view, and anything in the view
// wider than it
const overflow = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement;
  if (de.scrollWidth > de.clientWidth) out.push("page " + de.scrollWidth + ">" + de.clientWidth);
  const v = [...document.querySelectorAll("main.view")].find((m) => !m.hidden);
  if (v && v.scrollWidth > v.clientWidth) out.push(v.id + " " + v.scrollWidth + ">" + v.clientWidth);
  const top = document.querySelector(".top");
  if (top && top.scrollWidth > top.clientWidth) out.push("header " + top.scrollWidth + ">" + top.clientWidth);
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the text size", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-text-size-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, theme, size, posts, { width = 900, height = 520, zoom = 1, url = "/" } = {}) => {
      const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: zoom, reducedMotion: "reduce", colorScheme: theme });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", server(lang, theme, size, posts));
      await page.goto("http://magpie.test" + url);
      return page;
    };

    for (const [lang, name, toast] of [["en", "Text size", "Text size 125%"], ["zh", "文字大小", "文字大小 125%"]]) {
      await t.test(lang + ": picked, kept, and stepped with the keys", async () => {
        const posts = [];
        const page = await open(lang, "light", 100, posts);
        const mod = await page.evaluate(() => /^Mac/.test(navigator.platform) ? "Meta" : "Control");
        assert.equal(await zoomVar(page), "1");
        await page.locator("#prefs").click();
        const segs = page.locator("#textSizeSegs .opt");
        await segs.first().waitFor();
        assert.equal((await page.locator("#textSizeRow .name").textContent()).trim(), name);
        assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), ["100%", "110%", "125%", "150%"]);
        assert.equal(await segs.nth(0).evaluate((b) => b.classList.contains("on")), true, "100% starts picked");
        const sub = await page.locator("#textSizeSub").textContent();
        assert(sub.includes(mod === "Meta" ? "⌘0" : "Ctrl+0"), sub);

        // scrolled a little (a real wheel), picking 125% moves nothing
        await page.locator("#textSizeSegs").hover();
        for (let i = 0; i < 20 && !(await settingsTop(page)); i++) { await page.mouse.wheel(0, 30); await page.waitForTimeout(20); }
        const before = await settingsTop(page);
        assert(before > 0, "the settings list must scroll");
        await segs.nth(2).click();
        await page.locator("#textSizeSegs .opt.on", { hasText: "125%" }).waitFor();
        await page.waitForTimeout(300);
        assert.equal(await settingsTop(page), before, "picking a text size must not scroll the settings page");
        assert.deepEqual(posts, [125]);
        assert.equal(await zoomVar(page), "1.25");

        // kept: a reload has it from boot.js before the first paint
        await page.reload();
        assert.equal(await zoomVar(page), "1.25");
        await page.locator("#prefs").click();
        await page.locator("#textSizeSegs .opt.on", { hasText: "125%" }).waitFor();

        // the keys: + to 150, + again stays there, − to 125, 0 back to 100
        await page.keyboard.press(mod + "+Equal");
        await page.locator("#textSizeSegs .opt.on", { hasText: "150%" }).waitFor();
        await page.keyboard.press(mod + "+Equal");
        await page.keyboard.press(mod + "+Minus");
        await page.locator("#textSizeSegs .opt.on", { hasText: "125%" }).waitFor();
        assert.equal((await page.locator("#status").textContent()).trim(), toast);
        await page.keyboard.press(mod + "+Digit0");
        await page.locator("#textSizeSegs .opt.on", { hasText: "100%" }).waitFor();
        assert.deepEqual(posts, [125, 150, 125, 100]);
        assert.equal(await zoomVar(page), "1");
        // the other modifier is not the shortcut
        await page.keyboard.press((mod === "Meta" ? "Control" : "Alt") + "+Equal");
        await page.waitForTimeout(100);
        assert.deepEqual(posts, [125, 150, 125, 100]);
        // a save of the page's other settings leaves it be
        await page.locator("#themeSegs .opt").nth(2).click();
        await page.waitForTimeout(200);
        await page.reload();
        await page.locator("#prefs").click();
        await page.locator("#textSizeSegs .opt.on", { hasText: "100%" }).waitFor();
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });

      await t.test(lang + ": the panel", async () => {
        const posts = [];
        const page = await open(lang, "light", 110, posts, { width: 440, height: 560, url: "/?mode=panel" });
        const mod = await page.evaluate(() => /^Mac/.test(navigator.platform) ? "Meta" : "Control");
        await page.locator("#view-agents").waitFor();
        assert.equal(await zoomVar(page), "1.1");
        await page.keyboard.press(mod + "+Equal");
        await page.waitForTimeout(200);
        await page.keyboard.press(mod + "+Digit0");
        await page.waitForTimeout(200);
        assert.deepEqual(posts, [125, 100]);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });

      await t.test(lang + ": 100% picked at the foot of the page leaves no blank under it", async () => {
        // #911: at 150% scrolled to the foot, a click on 100% held the
        // control where it was as the webview zoomed out, and the page grew
        // a blank foot to hold it; Ctrl+0 went to the top. The page laid out
        // anew is the browser's to fit: no room, nothing scrolled.
        const posts = [];
        // emo172's window: 1440×900 points, 960×600 CSS pixels at 150%
        const page = await open(lang, "light", 150, posts, { width: 960, height: 600, zoom: 1.5 });
        await page.locator("#prefs").click();
        const segs = page.locator("#textSizeSegs .opt");
        await segs.first().waitFor();
        const v = page.locator("#view-settings");
        await v.hover();
        // down as far as the control is still in sight: to the foot, or the
        // control at the view's top
        for (let i = 0; i < 400; i++) {
          if (await v.evaluate((v) => v.scrollTop >= v.scrollHeight - v.clientHeight - 1
            || document.querySelector("#textSizeSegs").getBoundingClientRect().top < v.getBoundingClientRect().top + 60)) break;
          await page.mouse.wheel(0, 40);
          await page.waitForTimeout(20);
        }
        await page.waitForTimeout(300);
        const before = await v.evaluate((v) => v.scrollTop);
        assert(before > 0, "the settings page must scroll");
        await segs.nth(0).click();
        await page.locator("#textSizeSegs .opt.on", { hasText: "100%" }).waitFor();
        // the webview zooms out only once Go has the post (textsize.go), by
        // when the page has drawn the click: the Mac header back at 50px. A
        // resize before that frame is clamped to a view 17px too tall, which
        // shows once the page runs past the window's foot (bd33e575's font
        // rows made Settings that long)
        for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(20);
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        // the webview's zoom out: the same window is half as many CSS pixels again
        await page.setViewportSize({ width: 1440, height: 900 });
        await page.waitForTimeout(600);
        const foot = await v.evaluate((v) => {
          const kids = [...v.children].filter((c) => !c.classList.contains("view-room") && c.getClientRects().length);
          const end = Math.max(...kids.map((c) => c.getBoundingClientRect().bottom));
          return { room: !!v.querySelector(":scope > .view-room"), blank: v.getBoundingClientRect().bottom - end, top: v.scrollTop, max: v.scrollHeight - v.clientHeight };
        });
        assert.deepEqual(posts, [100]);
        assert.equal(foot.room, false, "no room may be left at the foot");
        // a page that still scrolls reaches the window's foot (one shorter has its own)
        assert(foot.max < 1 || foot.blank < 60, `a blank of ${foot.blank}px under the page`);
        // nothing scrolled: where it was, or as far down as the page now goes
        assert(Math.abs(foot.top - Math.min(before, foot.max)) < 2, `scrolled from ${before} to ${foot.top} (max ${foot.max})`);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });

      for (const theme of ["light", "dark"]) {
        await t.test(lang + ", " + theme + ": nothing runs off the side at 150%", async () => {
          // the smallest window at 150% is 840×630 points, 560×420 CSS pixels
          for (const [zoom, size] of [[1, 100], [1.5, 150]]) {
            const page = await open(lang, theme, size, [], { width: Math.round(840 / zoom), height: Math.round(630 / zoom), zoom });
            for (const v of ["agents", "providers", "gateway", "routing", "usage", "library", "settings"]) {
              await page.locator(v === "settings" ? "#prefs" : `[data-view="${v}"]`).click();
              await page.waitForTimeout(250);
              assert.deepEqual(await overflow(page), [], `${v} at ${size}%`);
              assert.equal(await page.locator("#status.err").count(), 0, `${v}: ${await page.locator("#status").textContent()}`);
              if (v === "agents" || v === "settings") shots.push([`${lang}-${theme}-${size}-${v}`, await page.screenshot()]);
            }
            if (await page.evaluate(() => document.body.classList.contains("mac"))) {
              // the header in points stays 50 tall, as the traffic lights want
              const h = await page.locator(".top").evaluate((e) => e.getBoundingClientRect().height);
              assert(Math.abs(h * zoom - 50) < 1, `header ${h}px at ${size}%`);
            }
            assert.deepEqual(page.errors, []);
            await page.context().close();
          }
          // the panel, as wide as it always is in CSS pixels
          const panel = await open(lang, theme, 150, [], { width: 440, height: 560, zoom: 1.5, url: "/?mode=panel" });
          await panel.locator("#view-agents").waitFor();
          await panel.waitForTimeout(250);
          assert.deepEqual(await overflow(panel), [], "the panel at 150%");
          shots.push([`${lang}-${theme}-150-panel`, await panel.screenshot()]);
          await panel.context().close();
        });
      }
    }
  });
}
