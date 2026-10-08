// Run with Node's test runner and Playwright on the module path; see README.md.
// Web pages that may call magpie (#1051, SkyAerope: a page on
// http://localhost:3000 couldn't read the gateway's answers): Settings ›
// Network lists them by origin. One is added from a field and its Add
// button (or Enter), saved as the gateway cleaned it, and taken away with
// its Remove; a wildcard or a URL that isn't an origin is refused, said in
// the page's language, and nothing is saved. At a 440px panel every row
// fits, and no click moves the page. English and Chinese, Chromium and
// WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "/config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    lan: false, lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// what the gateway does with a list: cleans it, or refuses it as
// internal/settings/origins.go does
function clean(list) {
  const out = [];
  for (const raw of list) {
    if (raw.includes("*")) return { error: `"${raw}": name each origin; a wildcard would let every web page use magpie` };
    let u;
    try { u = new URL(raw); } catch { u = null; }
    if (!u || !/^https?:$/.test(u.protocol) || (u.pathname !== "/" && u.pathname !== "")) return { error: `"${raw}" is not an origin: write it as http://localhost:3000 or https://app.example.com` };
    if (!out.includes(u.origin)) out.push(u.origin);
  }
  return { origins: out };
}

function server(lang, posts) {
  const s = settingsPayload({ lang, corsOrigins: [] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light"};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/settings/cors") {
      const body = route.request().postDataJSON();
      posts.push(body.origins);
      const c = clean(body.origins);
      if (c.error) return json({ error: c.error }, 400);
      s.corsOrigins = c.origins;
      return json(s);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    head: "Web pages", name: "Web pages that may call magpie", add: "Add", remove: "Remove",
    sub: "Scripts on these origins can call the gateway from a browser, each request with a gateway key from Gateway. A page from any other site is refused; pages on this computer are not",
    wildcard: "Name each origin: a wildcard would let every web page use magpie",
    notOrigin: "Write an origin as http://localhost:3000 or https://app.example.com",
  },
  zh: {
    head: "网页", name: "允许调用 magpie 的网页", add: "添加", remove: "移除",
    sub: null, wildcard: null, notOrigin: null, // read from i18n.js below
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": web pages that may call magpie are listed by origin (#1051)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const posts = [];
        const page = await (await browser.newContext({ viewport: { width: 440, height: 520 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://127.0.0.1:4999/?view=settings&tab=network");
        const box = page.locator("#corsList");
        await box.locator(".rule-row").waitFor();
        const tr = (s) => page.evaluate((s) => t(s), s);
        const w = { ...words[lang] };
        for (const k of ["head", "name", "add", "remove", "sub", "wildcard", "notOrigin"]) {
          const en = words.en[k];
          const got = await tr(en);
          if (lang !== "en") assert.notEqual(got, en, `"${en}" has no ${lang} translation`);
          if (w[k] === null) w[k] = got;
          else assert.equal(got, w[k]);
        }
        const head = box.locator("xpath=preceding-sibling::div[contains(@class,'row-head')][1]");
        assert.equal((await head.textContent()).trim(), w.head);
        const row = box.locator(".rule-row");
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        assert.equal((await row.locator(".sub").textContent()).trim(), w.sub);

        // every row fits the panel: nothing cut, nothing past the edge
        const fits = async () => {
          const bad = await box.evaluate((b) => {
            const out = [];
            const right = b.getBoundingClientRect().right + 0.5;
            for (const e of b.querySelectorAll(".row, input, button, .name, .sub")) {
              const r = e.getBoundingClientRect();
              if (r.right > right || r.width === 0) out.push(e.className + " " + r.right + ">" + right);
              if (e.matches("button, .name") && e.scrollWidth > e.clientWidth + 1) out.push("cut: " + e.textContent);
            }
            if (document.documentElement.scrollWidth > innerWidth) out.push("page scrolls sideways");
            return out;
          });
          assert.deepEqual(bad, []);
        };
        await fits();
        const field = row.locator("input");
        const add = row.locator("button", { hasText: w.add });
        const view = page.locator("#view-settings");
        // where the view rests once the frame is drawn
        const scroll = () => view.evaluate((v) => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(() => ok(v.scrollTop)))));
        // scrolled with the wheel, as a reader would (a script's scroll is
        // put back), until the field is well inside the view
        await view.hover();
        for (let i = 0; i < 40; i++) {
          const r = await field.boundingBox();
          if (r && r.y > 60 && r.y + r.height < 460 && (await scroll()) > 0) break;
          await page.mouse.wheel(0, 120);
          await page.waitForTimeout(30);
        }
        assert((await scroll()) > 0, "the settings must be scrolled to the field");

        // added from the field: saved as the gateway cleaned it
        await field.fill("http://LocalHost:3000/");
        let before = await scroll();
        await add.click();
        await box.locator(".row:not(.rule-row) .name", { hasText: "http://localhost:3000" }).waitFor();
        assert.deepEqual(posts.at(-1), ["http://LocalHost:3000/"]);
        assert.equal(await row.locator("input").inputValue(), "", "the field is left empty for the next one");
        assert.equal(await scroll(), before, "Add moved the page");
        // and with Enter
        await row.locator("input").fill("https://app.example.com");
        await row.locator("input").press("Enter");
        await box.locator(".row:not(.rule-row) .name", { hasText: "https://app.example.com" }).waitFor();
        assert.deepEqual(posts.at(-1), ["http://localhost:3000", "https://app.example.com"]);
        await fits();

        // refused, in the page's language, and nothing saved
        for (const [bad, says] of [["*", w.wildcard], ["localhost:4000/v1", w.notOrigin]]) {
          await row.locator("input").fill(bad);
          before = await scroll();
          await row.locator("button", { hasText: w.add }).click();
          await page.waitForFunction((s) => document.querySelector("#corsList .rule-row .sub.err")?.textContent.trim() === s, says);
          assert.equal(await row.locator("input").inputValue(), bad, "what the user wrote stays, to be corrected");
          assert.equal(await box.locator(".row:not(.rule-row)").count(), 2);
          assert.equal(await scroll(), before, "a refusal moved the page");
          await fits();
        }

        // taken away with its Remove
        const gone = box.locator(".row:not(.rule-row)").filter({ hasText: "http://localhost:3000" });
        before = await scroll();
        await gone.locator("button", { hasText: w.remove }).click();
        await page.waitForFunction(() => document.querySelectorAll("#corsList .row:not(.rule-row)").length === 1);
        assert.deepEqual(posts.at(-1), ["https://app.example.com"]);
        assert.equal(await scroll(), before, "Remove moved the page");
        assert.equal((await row.locator(".sub").textContent()).trim(), w.sub, "a save clears the refusal");

        const border = await box.locator(".row").first().evaluate((r) => getComputedStyle(r).borderLeftWidth);
        assert(border === "0px" || border === "", "no left border accent: " + border);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
