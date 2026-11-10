// Run with Node's test runner and Playwright on the module path; see README.md.
// netube99 on X: masking is switched on in Plugins › Built-in, set in
// Settings › Privacy and seen on Insights › Masking, and none of the three
// leads to the others. A built-in with settings (its manifest's SetAt) now
// has, under its row, Settings › <tab> and its page while it is on
// (featGo); its settings, while it is off, say so first and turn it on
// (featOff); Masking's head goes back to Settings › Privacy, and its row
// says so when nothing is chosen to mask. Settings
// hidden while their plugin is off (Observability) get no link then.
// Routing groups on while Routing is off have no page (theirs is
// Routing's): their row says so and turns Routing on.
// Every language, Chromium and WebKit, at 1100 and 420 wide, nothing
// running off to the side; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s] ?? s);

function fixture(lang, { redact = true, otel = true, masks = true, routing = true } = {}) {
  const st = { redact, otel, routing, groups: true };
  const posts = [];
  const row = (id, name, sub, on, more) => ({ id, name, sub, on, chosen: on, source: "preset", state: on ? "active" : "off", inUse: 0, ...more });
  const builtins = () => [
    row("redact", "Privacy masking", "Secrets, personal data and your own words go to vendors as placeholders, and come back as they were", st.redact,
      { setAt: "privacy #redactList", page: "masking", ...(st.redact ? { inUse: 1, use: "Masking is on" } : {}) }),
    row("otel", "Observability", "Send each request's trace to an OpenTelemetry collector", st.otel, { setAt: "otel #otelList" }),
    row("library", "Library", "Instructions, MCP servers, skills and hooks, kept once and given to every agent", true, { page: "library" }),
    row("routing", "Routing", "Each request as the gateway routes it", st.routing, {}),
    row("groups", "Routing groups", "Several models agents pick as one", st.groups, { opens: "groups" }),
  ];
  const settings = () => ({
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    redact: st.redact && masks, redactPersonal: false, redactWords: [], redactRules: [], redactKinds: {},
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3999", port: 3999,
    proxyNow: "none", proxySource: "none", vision: "", imageGen: "", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], searchChoices: [], searchAPIs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    builtins: builtins(),
  });
  const route = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      const off = builtins().filter((f) => !f.on).map((f) => f.id);
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", textSize: 100, featuresOff: off })};` });
    }
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/plugins/builtin") {
      if (req.method() !== "POST") return json(builtins());
      const b = req.postDataJSON();
      posts.push(b);
      if (b.id in st) st[b.id] = b.on;
      return json(builtins());
    }
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], gateway: { running: true } });
    if (url.pathname === "/api/masking/records") return json({ records: [], more: false });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
  return { route, posts };
}

const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
const featRow = (page, id) => page.locator(`#builtinList .feat-row[data-id="${id}"]`);
// nothing in a box reaches past its right edge or the window's
const past = (box) => box.evaluate((b) => {
  const r = b.getBoundingClientRect();
  return [...b.querySelectorAll("*")].filter((e) => e.getBoundingClientRect().right > Math.min(r.right, innerWidth) + 1).map((e) => e.className || e.tagName);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a built-in's switch, settings and page lead to each other", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-builtin-places-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, width, where, opts) => {
      const f = fixture(lang, opts);
      const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
      await page.route("**/*", f.route);
      await page.goto("http://magpie.test/" + where);
      return { page, posts: f.posts };
    };
    const builtinTab = async (page) => {
      const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
      await page.locator(side ? ".nav-sub:not(.leaving) .nav-s" : "#view-plugins .lib-tabs .opt").first().click();
      await featRow(page, "redact").waitFor();
    };
    for (const lang of LANGS) {
      const w = (s) => tr(lang, s);
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px: masking's Built-in row goes to its settings and its page`, async () => {
          for (const link of [0, 1]) {
            const { page } = await open(lang, width, "?view=plugins");
            await builtinTab(page);
            const line = featRow(page, "redact").locator(".who .feat-go");
            assert.equal((await line.textContent()).trim(), w("Settings") + " › " + w("Privacy") + " · " + w("Masking"));
            assert.equal(await wide(page), 0);
            assert.deepEqual(await past(featRow(page, "redact")), []);
            if (!link) shots.push([`${lang}-${width}-builtin`, await featRow(page, "redact").screenshot()]);
            // Observability on: its settings; Library: a page and no settings, the page
            assert.equal((await featRow(page, "otel").locator(".feat-go").textContent()).trim(), w("Settings") + " › " + w("Observability"));
            assert.equal((await featRow(page, "library").locator(".feat-go").textContent()).trim(), w("Library"));
            await line.locator("button.link").nth(link).click();
            if (link) await page.locator("#view-masking #maskCard .msk-head").waitFor();
            else await page.locator("#view-settings #redactList .row.flash").waitFor();
            assert.deepEqual(page.errors, []);
            await page.context().close();
          }
        });
        await t.test(`${lang} at ${width}px: on with nothing to mask, masking's Built-in row says so`, async () => {
          const { page } = await open(lang, width, "?view=plugins", { masks: false });
          await builtinTab(page);
          const line = featRow(page, "redact").locator(".who .feat-go");
          await line.locator(".warn").waitFor();
          assert.equal((await line.textContent()).trim(), w("Nothing is masked yet") + " · " + w("Settings") + " › " + w("Privacy") + " · " + w("Masking"));
          assert.equal(await wide(page), 0);
          assert.deepEqual(await past(featRow(page, "redact")), []);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: groups on with Routing off say their page went with it, and turn it on`, async () => {
          const { page, posts } = await open(lang, width, "?view=plugins", { routing: false });
          await builtinTab(page);
          const line = featRow(page, "groups").locator(".who .feat-go");
          assert.equal((await line.textContent()).trim(), w("Their page is on Routing, which is off") + " · " + w("Turn on Routing"));
          assert.equal(await wide(page), 0);
          assert.deepEqual(await past(featRow(page, "groups")), []);
          if (width === 420) shots.push([`${lang}-groups-routing-off`, await featRow(page, "groups").screenshot()]);
          await line.locator("button.link").click();
          await featRow(page, "groups").locator(".feat-go .warn").waitFor({ state: "detached" });
          assert.deepEqual(posts, [{ id: "routing", on: true }]);
          assert.equal(await featRow(page, "routing").locator(".lib-switch").getAttribute("aria-checked"), "true");
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: off, Built-in goes to its settings only, and they say it is off`, async () => {
          const { page, posts } = await open(lang, width, "?view=plugins", { redact: false, otel: false });
          await builtinTab(page);
          const line = featRow(page, "redact").locator(".who .feat-go");
          assert.equal((await line.textContent()).trim(), w("Settings") + " › " + w("Privacy"));
          // Observability's settings go with it
          assert.equal(await featRow(page, "otel").locator(".feat-go").count(), 0);
          await line.locator("button.link").click();
          const off = page.locator("#redactList .row.feat-off");
          await off.waitFor();
          assert.equal(await page.locator("#redactList .row").first().evaluate((r) => r.classList.contains("feat-off")), true);
          assert.equal((await off.locator(".name").textContent()).trim(), w("Off in Plugins › Built-in"));
          assert.equal((await off.locator(".sub").textContent()).trim(), w("Nothing is masked until it is on. Choosing anything to mask below turns it on"));
          assert.equal(await wide(page), 0);
          assert.deepEqual(await past(off), []);
          shots.push([`${lang}-${width}-settings-off`, await off.screenshot()]);
          await off.locator("button", { hasText: w("Turn on") }).click();
          await off.waitFor({ state: "detached" });
          assert.deepEqual(posts, [{ id: "redact", on: true }]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: on, Privacy has no off row`, async () => {
          const { page } = await open(lang, width, "?view=settings&tab=privacy");
          await page.locator("#redactList .row").first().waitFor();
          assert.equal(await page.locator("#redactList .row.feat-off").count(), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: Masking's head goes back to Settings › Privacy`, async () => {
          const { page } = await open(lang, width, "?view=masking");
          const head = page.locator("#maskCard .msk-head");
          const set = head.locator("button.link", { hasText: w("Settings") + " › " + w("Privacy") });
          await set.waitFor();
          assert.equal(await wide(page), 0);
          assert.deepEqual(await past(head), []);
          // its alert is in Settings › Privacy now, not on this page
          assert.equal(await page.locator("#maskPrefs, #maskAlertRow:visible").count(), 0);
          shots.push([`${lang}-${width}-masking`, await head.screenshot()]);
          await set.click();
          await page.locator("#view-settings #redactList .row.flash").waitFor();
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
    }
  });
}
