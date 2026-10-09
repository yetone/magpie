// Run with Node's test runner and Playwright on the module path; see README.md.
// #1385 (shenghsi): Codex isn't connected, yet magpie set openai_base_url for
// it so a request its ChatGPT account has no allowance for goes on to the
// user's other ChatGPT accounts on in magpie. The row says it goes through
// magpie for that alone, its title says what turns it off, and a button
// opens Providers › Codex, where those accounts are switched off. A Codex
// not failing over says what it said before. In every language, at a narrow
// width too, in Chromium and WebKit; no click moves the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "gpt-5.5", label: "GPT-5.5" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }];
const state = () => ({
  agents: [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", failover: true, fields: [{ key: "model", label: "model", value: "gpt-5.5", options }] },
    { id: "codex@wsl:Ubuntu-24.04", name: "Codex", icon: "generic", path: "/fixture/wsl-codex", fields: [{ key: "model", label: "model", value: "gpt-5.5", options }] },
  ],
  profiles: [],
});

function server(lang) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...state(), settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [codexProvider], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// three ChatGPT accounts on, as the reporter's
const codexProvider = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "smart",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS",
    logins: [
      { user: "me@example.com", plan: "PLUS", active: true, on: true },
      { user: "spare1@example.com", plan: "PLUS", on: true },
      { user: "spare2@example.com", plan: "PRO", on: true },
    ],
  },
};

const said = "Not connected · goes through magpie only to fail over to your other ChatGPT accounts";
const title = "magpie set openai_base_url in {agent}'s config.toml, so a request the ChatGPT account {agent} is signed in to has no allowance for goes on to your other ChatGPT accounts on in magpie. {agent} keeps to its own models. To stop it, switch those accounts off under Providers › Codex; connecting {agent} adds magpie's models.";
const button = "ChatGPT accounts";
const plain = "Not connected · {agent} uses its own settings";
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [980, 420]) {
      test(`${engine} ${lang} ${width}px: a Codex there for failover alone says so and how to stop it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], dialogs = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row("codex")} .ag-st`).waitFor();
        const tr = (k) => page.evaluate(([k, lang]) => (lang === "en" ? k : I18N[lang]?.[k]), [k, lang]);
        for (const k of [said, title, button]) assert.ok(await tr(k), `${lang} has ${k.slice(0, 40)}`);
        const want = (await tr(said));
        const wantTitle = (await tr(title)).replaceAll("{agent}", "Codex");
        const st = page.locator(`${row("codex")} .ag-st`);
        assert.ok((await st.textContent()).startsWith(want), await st.textContent());
        const go = st.locator("button.ag-acc");
        assert.equal(await go.textContent(), await tr(button));
        assert.equal(await go.getAttribute("title"), wantTitle);
        // the twin not failing over says what it always did
        assert.equal(await page.locator(`${row("codex@wsl:Ubuntu-24.04")} .ag-st`).textContent(),
          (await tr(plain)).replace("{agent}", "Codex"));
        assert.equal(await page.locator(`${row("codex@wsl:Ubuntu-24.04")} .ag-acc`).count(), 0);
        const wide = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(wide <= 0, `the page scrolls sideways by ${wide}px`);
        const box = await go.boundingBox();
        assert.ok(box && box.x >= 0 && box.x + box.width <= width, `button off screen: ${JSON.stringify(box)}`);
        const top = await page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
        await go.click();
        await page.waitForFunction(() => new URL(location.href).searchParams.get("view") === "providers");
        await page.waitForFunction(() => new URL(location.href).searchParams.get("edit") === "codex");
        // its editor, with the accounts to switch off, is what opens
        await page.waitForFunction(() => !document.querySelector("#modal").hidden && document.querySelector("#modal").innerText.startsWith("Codex"));
        assert.ok(await page.locator(".row.provider.selected[data-id=codex]").count(), "Codex is the one open");
        assert.deepEqual(dialogs, []);
        assert.deepEqual(errors, []);
        assert.ok(top.every((v) => v === 0 || v === undefined), `page was scrolled ${top}`);
      });
    }
  }
}
