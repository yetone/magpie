// Run with Node's test runner and Playwright on the module path; see README.md.
// #1433 (ObsidianArch02): with plainNames: true, plainOwnNames: false saved
// in settings.json, a restart that opened Routing before Settings showed
// "Names in agents' lists" as On. The choice was read from Settings' own
// read of the settings, which only opening Settings makes, though startup
// had already read them with the state. Opening Settings put it right, and
// the next restart brought On back. The provider editor's Names, prices & levels
// read the same thing. Here each page is opened first, with Off or "Not on
// names I set" saved, and the choice and the label beside it must say what
// is saved from the first drawing on: when the state comes in before the
// page, and when the page is drawn first (Routing's groups answering before
// the state). Opening Settings and coming back changes nothing, and nothing
// is written. Every language, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, saved, { stateDelay = 0 } = {}, posts) {
  const cur = { lang, theme: "light", ...saved };
  // the state carries the saved settings as /api/state does: the raw
  // settings.json, without Settings' extras (version, dir, gateway)
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { ...cur } };
  const models = [{ id: "sol-2026-preview-long", name: "My Sol", default: "sol-2026-preview-long", on: true, efforts: [], images: false }];
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const groups = { models: [{ id: "relay/sol-2026-preview-long", name: "My Sol", providerName: "Relay", icon: "generic" }], pools: [],
    groups: [{ id: "fast", name: "Fast", members: ["relay/sol-2026-preview-long"], routing: "order", ready: true, memberInfo: [{ id: "relay/sol-2026-preview-long", ready: true }] }] };
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (req.method() === "POST" && url.pathname.startsWith("/api/settings")) posts.push(url.pathname);
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      if (stateDelay) await new Promise((res) => setTimeout(res, stateDelay));
      return json(state);
    }
    if (url.pathname === "/api/settings") return json({ ...cur, version: "0.1.1140", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3999" });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function launch(t, engine, lang, width, saved, opts, posts) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${t.name.replace(/\W+/g, "-").slice(-40)}.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, saved, opts, posts));
  const tr = async (k, vars) => {
    const s = await page.evaluate(([k, lang]) => (lang === "en" ? k : I18N[lang]?.[k]), [k, lang]);
    assert.ok(s, `${lang} has ${k}`);
    return s.replace(/\{(\w+)\}/g, (_, n) => vars[n]);
  };
  return { page, errors, tr };
}

// what is picked, once it says want (the state can still be on its way)
async function picked(loc, want) {
  for (let i = 0; i < 100 && ((await loc.textContent().catch(() => "")) || "").trim() !== want; i++) await new Promise((r) => setTimeout(r, 20));
  assert.equal((await loc.textContent()).trim(), want);
}
async function says(loc, want) {
  for (let i = 0; i < 100 && (await loc.textContent().catch(() => "")) !== want; i++) await new Promise((r) => setTimeout(r, 20));
  assert.equal(await loc.textContent(), want);
}

const OFF = { plainNames: true, plainOwnNames: false };
const OWN = { plainOwnNames: true };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: Routing opened first shows the saved Off, and Settings and back keep it`, async (t) => {
        const posts = [];
        const { page, errors, tr } = await launch(t, engine, lang, width, OFF, {}, posts);
        await page.goto("http://magpie.test/?view=groups");
        await page.locator(".rt-group", { hasText: "Fast" }).waitFor();
        const set = page.locator(".rt-gnames");
        await set.locator(".segs").waitFor();
        await picked(set.locator(".opt.on"), await tr("Off"));
        assert.equal(await set.locator(".opt.on").count(), 1);
        // a group's editor says what its group is called there: no suffix
        await page.locator(".rt-group", { hasText: "Fast" }).locator("button", { hasText: await tr("Edit") }).click();
        const row = page.locator(".rt-gedit label", { hasText: new RegExp(`^${await tr("In agents’ lists")}$`) }).locator("xpath=following-sibling::div[1]");
        await says(row.locator(".hint"), (await tr("Agents’ lists show “{label}”", { label: "Fast" })) + " · " + (await tr("“· routing group” is set for every group, under the list")));
        // Settings says the same, and coming back still does
        await page.locator("#prefs").click();
        await page.locator("#plainNamesSegs .opt.on").waitFor();
        await picked(page.locator("#plainNamesSegs .opt.on"), await tr("Off"));
        // the groups tab is the sidebar's; the header's row reaches them from Routing
        const tab = page.locator('#nav button[data-view="groups"]');
        if (await tab.isVisible()) await tab.click();
        else { await page.locator('#nav button[data-view="routing"]').click(); await page.locator("#rtToGroups").click(); }
        await picked(page.locator(".rt-gnames .opt.on"), await tr("Off"));
        assert.deepEqual(posts, [], "nothing is written");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: Routing drawn before the state comes in follows the saved "Not on names I set"`, async (t) => {
        const posts = [];
        const { page, errors, tr } = await launch(t, engine, lang, width, OWN, { stateDelay: 600 }, posts);
        await page.goto("http://magpie.test/?view=groups");
        const set = page.locator(".rt-gnames");
        await set.locator(".segs").waitFor();
        await picked(set.locator(".opt.on"), await tr("Not on names I set"));
        assert.equal(await set.locator(".opt.on").count(), 1);
        assert.deepEqual(posts, [], "nothing is written");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: the provider editor opened first shows the saved Off and the name without its provider`, async (t) => {
        const posts = [];
        // the state answering after the editor is drawn: its line follows it
        const { page, errors, tr } = await launch(t, engine, lang, width, OFF, { stateDelay: 600 }, posts);
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider").click();
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: await tr("Names, prices & levels"), exact: true }).click();
        const sfx = page.locator(".mnames:not([hidden]) .msuffix");
        await sfx.waitFor();
        await says(sfx.locator(".hint"), await tr("Agents’ lists show “{label}”", { label: "My Sol" }));
        assert.deepEqual(posts, [], "nothing is written");
        assert.deepEqual(errors, []);
      });
    }
  }
}
