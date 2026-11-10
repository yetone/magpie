// Run with Node's test runner and Playwright on the module path; see README.md.
// Join with a password (EV_John_Liu on X): on a magpie shared on the
// network, Settings › Local network sets a password, and another computer
// opens <address>/join, types it and a name for itself, and gets a gateway
// key of its own, shown once, with its setup (Claude Code, Codex, another
// magpie, OpenAI-compatible) filled in. The join page is the gateway's own
// keypage.html, served under its Content-Security-Policy; the key never
// rides in an address. Chromium and WebKit, 390px, every language; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const fsp = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const html = fs.readFileSync(path.resolve(__dirname, "../../gateway/keypage.html"), "utf8");
const CSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";
const BASE = "http://192.168.1.20:3425";
const PW = "correct horse";
const KEY = "sk-magpie-joined-0123456789";
const ME = {
  name: "Studio Mac", readOnly: false, version: "dev", limits: [],
  usage: { today: { calls: 0 }, "7d": { calls: 0 }, "30d": { calls: 0 } }, topModels: [],
  models: [{ id: "plan/m1", label: "m1 · Plan" }],
};
const LOCALE = { en: "en-US", zh: "zh-CN", "zh-TW": "zh-TW", ja: "ja-JP", de: "de-DE" };
const TITLE = { en: "Join this magpie", zh: "加入这个 magpie", "zh-TW": "加入這個 magpie", ja: "この magpie に参加", de: "Diesem magpie beitreten" };

async function openJoin(browser, lang, { open = true } = {}) {
  const page = await (await browser.newContext({ viewport: { width: 390, height: 844 }, locale: LOCALE[lang], reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  const seen = { urls: [], errors: [], posts: [] };
  page.on("pageerror", (e) => seen.errors.push(e.message));
  page.on("request", (r) => seen.urls.push(r.url()));
  await page.route("**/*", (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/join") return route.fulfill({ contentType: "text/html; charset=utf-8", headers: { "Content-Security-Policy": CSP }, body: html });
    if (url.pathname === "/v1/magpie/join" && req.method() === "GET") return route.fulfill({ json: { open } });
    if (url.pathname === "/v1/magpie/join") {
      const body = req.postDataJSON();
      seen.posts.push(body);
      if (!open) return route.fulfill({ status: 404, json: { error: { message: "off" } } });
      if (body.password !== PW) return route.fulfill({ status: 401, json: { error: { message: "wrong password" } } });
      return route.fulfill({ status: 201, json: { key: KEY, id: "k9", name: body.name } });
    }
    if (url.pathname === "/v1/magpie/me") {
      if (req.headers().authorization !== "Bearer " + KEY) return route.fulfill({ status: 401, json: {} });
      return route.fulfill({ json: ME });
    }
    return route.fulfill({ status: 404, body: "" });
  });
  await page.goto(BASE + "/join");
  return { page, seen };
}

const assets = path.resolve(__dirname, "../assets");
const file = async (route, url) => {
  const f = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(f)];
  await route.fulfill({ body: await fsp.readFile(f), contentType });
};
function settingsServer(lang, posts, over) {
  const s = {
    theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.400", dir: "/config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], allowIPs: [], mtlsCerts: [],
    lan: true, lanURLs: [BASE], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, ...over,
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/settings/join") {
      const body = route.request().postDataJSON();
      posts.push(body);
      if (body.password && body.password.length < 8) return json({ error: "Use a password of at least 8 characters" }, 400);
      s.joinOn = !!body.password;
      return json(s);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    await file(route, url);
  };
}

const STRINGS = ["Join with a password", "Other computers open the join link and type this password to get a gateway key of their own and the setup for their agents",
  "New password", "Set", "Turn off joining", "Join link", "Use a password of at least 8 characters", "Use a password of at most 1024 bytes"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": join with a password (EV_John_Liu on X)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang + ": a computer joins and gets its key and setup", async () => {
        const { page, seen } = await openJoin(browser, lang);
        assert.equal(await page.locator("#join h1").textContent(), TITLE[lang]);
        assert.equal(await page.locator("#signin").isHidden(), true);
        // a wrong password is told so, and makes nothing
        await page.fill("#device", "Studio Mac");
        await page.fill("#pw", "guess-guess");
        await page.click("#joinGo");
        await page.locator("#joinErr").waitFor();
        const err = await page.locator("#joinErr").textContent();
        if (lang !== "en") assert.doesNotMatch(err, /Wrong password/);
        assert.equal(await page.locator("#info").isHidden(), true);
        await page.fill("#pw", PW);
        await page.click("#joinGo");
        await page.locator("#info").waitFor();
        assert.deepEqual(seen.posts.at(-1), { password: PW, name: "Studio Mac" });
        assert.equal(await page.locator("#name").textContent(), "Studio Mac");
        assert.equal(await page.locator("#onceKey").textContent(), KEY);
        assert.equal(await page.locator("#pw").inputValue(), "", "the password is taken out of the field");
        // another magpie's setup, with this address and the key
        await page.click(`#tabs [data-tab="magpie"]`);
        const snip = await page.locator("#snip pre").first().textContent();
        assert.equal(snip, `magpie provider add remote-magpie '${KEY}' url='${BASE}'`);
        await page.click(`#tabs [data-tab="claude"]`);
        assert.match(await page.locator("#snip pre").first().textContent(), new RegExp(`"ANTHROPIC_BASE_URL": "${BASE}"[\\s\\S]*"${KEY}"`));
        // no key or password in an address, nothing thrown, nothing wider than the phone
        for (const u of seen.urls) assert.ok(!u.includes(KEY) && !u.includes(encodeURIComponent(PW)) && !u.includes("guess"), u);
        assert.deepEqual(seen.errors, []);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
        // forgetting the key comes back to the join form, the key gone
        await page.click("#forget");
        await page.locator("#join").waitFor();
        assert.equal(await page.locator("#once").isHidden(), true);
        await page.context().close();
      });
    }

    await t.test("joining off: the form says so and sends nothing", async () => {
      const { page, seen } = await openJoin(browser, "zh", { open: false });
      await page.locator("#joinErr").waitFor();
      assert.match(await page.locator("#joinErr").textContent(), /没有开启密码加入/);
      assert.equal(await page.locator("#joinGo").isDisabled(), true);
      assert.deepEqual(seen.posts, []);
      await page.context().close();
    });

    await t.test("every language has the Settings strings", async () => {
      const page = await (await browser.newContext({ viewport: { width: 390, height: 640 } })).newPage();
      await page.route("**/*", settingsServer("en", [], {}));
      await watchSettingsReads(page);
      await page.goto("http://127.0.0.1:4999/?view=settings&tab=network");
      await settingsDrawn(page);
      for (const lang of ["zh", "zh-TW", "ja", "de"]) {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k] || I18N[lang][k] === k), [lang, STRINGS]);
        assert.deepEqual(missing, [], lang);
      }
      await page.context().close();
    });

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": Settings sets the password, shows the link, turns it off", async () => {
        const posts = [];
        const page = await (await browser.newContext({ viewport: { width: 390, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", settingsServer(lang, posts, {}));
        await watchSettingsReads(page);
        await page.goto("http://127.0.0.1:4999/?view=settings&tab=network");
        await settingsDrawn(page);
        const w = {};
        for (const s of STRINGS) w[s] = await page.evaluate((s) => t(s), s);
        const row = page.locator("#lanList .join-row");
        await row.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), w["Join with a password"]);
        assert.equal(await page.locator("#lanList .join-link-row").count(), 0);
        const field = row.locator("input.join-pw");
        assert.equal(await field.getAttribute("type"), "password");
        await reach(page, field);
        // too short: the gateway's word, in the page's language
        await field.fill("short");
        await row.locator("button", { hasText: w["Set"] }).click();
        await page.waitForFunction((m) => document.querySelector("#lanList .join-row .sub.err")?.textContent === m, w["Use a password of at least 8 characters"]);
        await page.locator("#lanList .join-row input.join-pw").fill(PW);
        await page.locator("#lanList .join-row button", { hasText: w["Set"] }).click();
        const link = page.locator("#lanList .join-link-row code");
        await link.waitFor();
        assert.equal(await link.textContent(), BASE + "/join");
        assert.deepEqual(posts.at(-1), { password: PW });
        assert.equal(await page.locator("#lanList .join-row input.join-pw").inputValue(), "", "the password isn't kept in the field");
        const bad = await page.evaluate(() => {
          const right = document.querySelector("#lanList").getBoundingClientRect().right + 0.5;
          return [...document.querySelectorAll("#lanList .join-row *, #lanList .join-link-row *")]
            .filter((e) => e.getBoundingClientRect().right > right && !e.closest("code")).map((e) => e.className)
            .concat(document.documentElement.scrollWidth > innerWidth ? ["page scrolls sideways"] : []);
        });
        assert.deepEqual(bad, []);
        await page.locator("#lanList .join-row button.join-off").click();
        await page.waitForFunction(() => !document.querySelector("#lanList .join-link-row"));
        assert.deepEqual(posts.at(-1), { password: "" });
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
