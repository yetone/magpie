// Run with Node's test runner and Playwright on the module path; see README.md.
// KoiLx0 on X: a client magpie had each agent switched on by hand before
// it took the remote's models. A Remote magpie's editor has a switch for
// that, "Connect new agents to this remote", under Advanced: off, and the
// fold closed, while the backend says it is off; sent as ticked; and with
// it on, Advanced opens on it ticked. In every language, both engines, at
// a narrow width with no sideways scroll. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const office = () => ({ id: "office", name: "Office", icon: "magpie", preset: "remote-magpie", chat: "http://office/v1", responses: "http://office/v1", anthropic: "http://office",
  models: [{ id: "lib/m1", name: "Chat", on: true }], agents: [], key: { set: true, masked: "sk-…ab12" } });
const presets = [{ id: "remote-magpie", name: "Remote magpie", icon: "magpie", kind: "remote", added: true, endpoint: "http://192.168.1.20:3425" }];

function server(lang, state, saves) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [state.p], presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/save") {
      const body = route.request().postDataJSON();
      saves.push(body);
      if (body.connectAgents != null) state.p = { ...state.p, connectAgents: body.connectAgents || undefined };
      return json({ providers: [state.p], presets, excluded: [], gateway: { running: true, window: true } });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: "Connect new agents to this remote",
  zh: "自动接入新 agent",
  "zh-TW": "自動接入新 agent",
  ja: "新しいエージェントを自動で接続する",
  de: "Neue Agenten automatisch verbinden",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a Remote magpie's switch to connect new agents, under Advanced", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, words] of Object.entries(L)) {
      await t.test(lang, async () => {
        const saves = [], state = { p: office() };
        const page = await (await browser.newContext({ viewport: { width: 420, height: 800 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state, saves));
        await page.goto("http://magpie.test/?view=providers");
        const ed = page.locator(".editor");
        const open = async () => {
          await page.locator('#providers .row[data-id="office"]').click();
          await ed.locator(".ehead b", { hasText: "Office" }).waitFor();
        };
        const save = () => Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/provider/save")), ed.locator(".bar button.primary").first().click()]);

        // off: under Advanced, closed
        await open();
        const adv = ed.locator("details.advanced");
        const sw = adv.locator(".connect-agents");
        assert.equal(await sw.count(), 1, "the switch is under Advanced");
        assert.equal(await sw.textContent(), words);
        assert.equal(await adv.evaluate((d) => d.open), false, "Advanced closed while it is off");
        assert.equal(await sw.locator("input").isChecked(), false);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true, "no sideways scroll");

        // ticked: sent so
        await adv.locator("summary").click();
        await sw.locator("input").check();
        await save();
        assert.equal(saves.at(-1).connectAgents, true);
        await page.keyboard.press("Escape");
        await ed.waitFor({ state: "detached" });

        // on: Advanced opens on it ticked; unticked, sent so
        await page.evaluate(() => { advanced = null; });
        await open();
        assert.equal(await adv.evaluate((d) => d.open), true, "Advanced opens while it is on");
        assert.equal(await sw.locator("input").isChecked(), true);
        await sw.locator("input").uncheck();
        await save();
        assert.equal(saves.at(-1).connectAgents, false);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
