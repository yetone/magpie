// Run with Node's test runner and Playwright on the module path; see README.md.
// #1908: Antigravity CLI's accounts kept to agy's own sign-in. Under its
// accounts a tick, Only switch agy's sign-in, says that using an account
// signs agy and the Antigravity app in to it; ticking posts
// provider/switchonly. Ticked, the Agents rows of agy and of the
// Antigravity app, not connected, say the account agy is signed in to and
// switch it as Claude Code's row does: login/switch for agy-cli, the line
// saying it switches accounts only. Unticked, they have no switch: a pick
// there would move magpie's order alone. In every language, at a narrow
// width too, in Chromium and WebKit; no click moves the page. Ticked,
// Switch agy's account automatically shows under it, off by default, and
// switching it on posts provider/autoswitch; unticked, it isn't there.
// A switch it holds back while agy or the app runs is said under it, and
// a switch refused for the same reason is said in the reader's language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const own = [{ value: "gemini-3-pro", label: "Gemini 3 Pro" }];
const agents = () => [
  { id: "agy", name: "Antigravity CLI", icon: "antigravity-color", path: "/fixture/agy", source: "", wired: false, fields: [{ key: "model", label: "model", value: "gemini-3-pro", options: own }] },
  { id: "antigravity", name: "Antigravity", icon: "antigravity-color", path: "/fixture/antigravity", source: "", wired: false, fields: [{ key: "model", label: "model", value: "gemini-3-pro", options: own }] },
];

function providersOf(w) {
  const agy = {
    id: "agy-cli", name: "Antigravity CLI", icon: "antigravity-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "smart",
    models: [{ id: "gemini-3-pro", name: "Gemini 3 Pro", on: true }], agents: [], fallback: [], headers: {}, keyList: [], switchOnly: w.switchOnly || undefined, autoSwitch: w.autoSwitch || undefined, agyWait: w.agyWait,
    account: {
      agent: "agy-cli", agentName: "Antigravity CLI", user: w.active,
      logins: ["work@example.com", "home@example.com"].map((user) => ({ user, active: user === w.active, on: true })),
    },
  };
  return { providers: [agy], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [{ id: "agy-cli", pid: "agy-cli", name: "Antigravity CLI", icon: "antigravity-color", spec: "@magpie-community/opencode-agy-cli" }] };
}

function server(lang, w, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: agents(), profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json(providersOf(w));
    if (req.method() === "POST" && ["/api/login/switch", "/api/provider/switchonly", "/api/provider/autoswitch"].includes(url.pathname)) {
      const body = req.postDataJSON();
      posts.push([url.pathname, body]);
      // what fail() answers for a provider.AgyRunning (api.go)
      if (url.pathname === "/api/login/switch" && w.busy) return route.fulfill({ status: 400, json: { error: "quit the Antigravity app, agy (pid 4312) first: …", code: "agy_running", said: K.refusedBoth, pids: "4312", plugin: "" } });
      if (url.pathname === "/api/login/switch") w.active = body.user;
      if (url.pathname === "/api/provider/switchonly") w.switchOnly = body.switchOnly;
      if (url.pathname === "/api/provider/autoswitch") w.autoSwitch = body.autoSwitch;
      return json(providersOf(w));
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const K = {
  head: "Switch accounts only",
  only: "Only switch {agent}'s sign-in",
  tip: "Using an account signs agy and the Antigravity app in to it. Requests on these accounts don't go through magpie.",
  onlyLine: "Not connected · switching accounts only",
  manage: "Manage accounts…",
  served: "magpie serves no requests on these accounts",
  auto: "Switch {agent}'s account automatically",
  autoTip: "When agy's account runs low, magpie signs agy in to the next one with room, once agy and the Antigravity app have quit.",
  note: "magpie only switches {agent}'s own sign-in between these accounts; no requests go through magpie.",
  waits: "{from} has used its allowance. magpie signs agy in to {to} once agy and the Antigravity app have quit",
  waitsPlugin: "{from} has used its allowance. magpie signs agy in to {to} when the Antigravity CLI plugin's agy has ended, within ten minutes",
  refusedBoth: "Quit the Antigravity app and agy (pid {pids}) first: while they run they keep the account they started on and write it back over a switch. Nothing was changed",
};
const row = (id) => `.row.agent[data-id="${id}"]`;

async function open(engine, lang, width, w, view, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, w, posts));
  await page.goto(`http://magpie.test/?view=${view}`);
  const tr = async (k, vars = {}) => {
    const s = await page.evaluate(([k, lang]) => (lang === "en" ? k : I18N[lang]?.[k]), [k, lang]);
    assert.ok(s, `${lang} has ${k.slice(0, 40)}`);
    return Object.entries(vars).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, v), s);
  };
  const scrolled = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).concat(document.scrollingElement.scrollTop));
  return { page, errors, posts, tr, scrolled };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: Antigravity CLI's tick keeps its accounts to agy's sign-in`, async (t) => {
        const w = { active: "work@example.com" };
        const { page, errors, posts, tr, scrolled } = await open(engine, lang, width, w, "providers", t);
        for (const k of Object.values(K)) await tr(k);
        const prow = page.locator(".row.provider", { hasText: "Antigravity CLI" }).first();
        await prow.waitFor({ timeout: 60000 });
        await prow.click();
        await page.locator(".editor .acc").first().waitFor();
        const only = page.locator(".editor .accts .switch-only label");
        assert.equal((await only.textContent()).trim(), await tr(K.only, { agent: "agy" }));
        assert.equal(await only.getAttribute("title"), await tr(K.tip));
        assert.equal(await only.locator("input").isChecked(), false);
        assert.equal(await page.locator(".editor .accts .auto-switch").count(), 0, "no automatic switch while Only switch is off");
        await only.scrollIntoViewIfNeeded();
        const box = await only.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the tick fits across ${width}`);
        const before = await scrolled();
        await only.locator("input").click();
        await page.waitForFunction(() => document.querySelector(".editor .accts .switch-only input")?.checked === true);
        assert.deepEqual(posts, [["/api/provider/switchonly", { id: "agy-cli", switchOnly: true }]]);
        assert.deepEqual(await scrolled(), before, "the page doesn't move");
        // ticked: Switch automatically under it, off
        const auto = page.locator(".editor .accts .auto-switch .as-head");
        await auto.waitFor();
        assert.equal((await auto.locator(".as-name").textContent()).trim(), await tr(K.auto, { agent: "agy" }));
        assert.equal(await auto.getAttribute("title"), await tr(K.autoTip));
        const sw = auto.locator("button.lib-switch");
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        const ab = await auto.boundingBox();
        assert.ok(ab.x >= 0 && ab.x + ab.width <= width, `the switch fits across ${width}`);
        assert.equal(await page.locator(".editor .accts .auto-switch .as-wait").count(), 0, "nothing waits");
        await sw.click();
        await page.waitForFunction(() => document.querySelector(".editor .accts .auto-switch button.lib-switch")?.getAttribute("aria-checked") === "true");
        assert.deepEqual(posts[1], ["/api/provider/autoswitch", { id: "agy-cli", autoSwitch: true }]);
        assert.deepEqual(await scrolled(), before, "the page doesn't move");
        // the field's note says what Only switch does, not the gateway's order
        const note = page.locator(".editor .fld", { has: page.locator(".accts") }).first().locator(".hint.expl");
        await page.waitForFunction(() => document.querySelector(".editor .accts .switch-only input")?.checked === true);
        assert.equal(await note.textContent(), await tr(K.note, { agent: "Antigravity CLI" }), "the accounts note says magpie only switches");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a switch agy holds back is said under Switch automatically, and a refused one in the reader's language`, async (t) => {
        const w = { active: "work@example.com", switchOnly: true, autoSwitch: true, busy: true, agyWait: { from: "work@example.com", to: "home@example.com", app: true } };
        const { page, errors, tr } = await open(engine, lang, width, w, "providers", t);
        const prow = page.locator(".row.provider", { hasText: "Antigravity CLI" }).first();
        await prow.waitFor({ timeout: 60000 });
        await prow.click();
        const wait = page.locator(".editor .accts .auto-switch .as-wait");
        await wait.waitFor();
        assert.equal(await wait.textContent(), await tr(K.waits, { from: "work@example.com", to: "home@example.com" }));
        const wb = await wait.boundingBox();
        assert.ok(wb.x >= 0 && wb.x + wb.width <= width, `the line fits across ${width}`);
        // the plugin's agy alone: it ends by itself
        w.agyWait = { from: "work@example.com", to: "home@example.com", plugin: [60] };
        await page.evaluate(() => accountAction("provider/autoswitch", { id: "agy-cli", autoSwitch: true }, ""));
        await page.waitForFunction((s) => document.querySelector(".editor .accts .auto-switch .as-wait")?.textContent === s, await tr(K.waitsPlugin, { from: "work@example.com", to: "home@example.com" }));
        // a switch refused while agy runs says what to quit, translated
        await page.evaluate(() => accountAction("login/switch", { agent: "agy-cli", user: "home@example.com" }, ""));
        const said = await tr(K.refusedBoth, { pids: "4312" });
        await page.waitForFunction((s) => [...document.querySelectorAll("#status, .editor .err, .editor-error")].some((e) => e.textContent.includes(s)), said);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: agy's and the Antigravity app's rows switch agy's account`, async (t) => {
        const w = { active: "work@example.com", switchOnly: true };
        const { page, errors, posts, tr, scrolled } = await open(engine, lang, width, w, "agents", t);
        for (const id of ["agy", "antigravity"]) {
          const st = page.locator(`${row(id)} .ag-st`);
          await st.locator("button.ag-who").waitFor({ timeout: 60000 });
          assert.equal(await st.locator(".ag-st-t").textContent(), await tr(K.onlyLine), id);
          assert.equal(await st.locator("button.ag-who .v").textContent(), "work@example.com", id);
        }
        const wide = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(wide <= 0, `the page scrolls sideways by ${wide}px`);
        const top = await scrolled();
        const who = page.locator(`${row("antigravity")} .ag-st button.ag-who`);
        await who.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-head").textContent(), await tr(K.head));
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["work@example.com", "home@example.com", await tr(K.only, { agent: "agy" }), await tr(K.manage)]);
        const mb = await menu.boundingBox();
        assert.ok(mb.x >= 0 && mb.x + mb.width <= width, `menu off screen: ${JSON.stringify(mb)}`);
        await menu.locator(".pm-item", { hasText: "home@example.com" }).click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="agy"] .ag-who .v')?.textContent === "home@example.com");
        assert.deepEqual(posts, [["/api/login/switch", { agent: "agy-cli", user: "home@example.com" }]]);
        assert.equal(await page.locator(`${row("antigravity")} .ag-who .v`).textContent(), "home@example.com");
        assert.deepEqual(await scrolled(), top, "the page doesn't move");
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: with Only switch off, agy's rows have no account switch`, async (t) => {
    const { page, errors } = await open(engine, "en", 1100, { active: "work@example.com" }, "agents", t);
    await page.locator(`${row("antigravity")} .ag-st`).waitFor({ timeout: 60000 });
    // the rows are drawn again once the providers are read: two frames on
    await page.waitForResponse((r) => r.url().endsWith("/api/providers")).catch(() => {});
    await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
    assert.equal(await page.locator(`${row("agy")} .ag-who`).count(), 0);
    assert.equal(await page.locator(`${row("antigravity")} .ag-who`).count(), 0);
    assert.deepEqual(errors, []);
  });
}
