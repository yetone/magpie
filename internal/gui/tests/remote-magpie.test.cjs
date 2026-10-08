// Run with Node's test runner and Playwright on the module path; see README.md.
// Another computer's magpie is added from the Relays with its address and
// key alone: the Remote magpie editor asks for the address above the key,
// with a hint on where the other magpie shows it and that each request
// goes on in the API the agent spoke. Adding without one says so in its
// own words and sends nothing; with one, the address goes as the preset's
// endpoint, however it was typed (the backend puts every API on it).
// Nothing moves the page. English and Chinese; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const presets = [
  { id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.com/v1", added: false },
  { id: "remote-magpie", name: "Remote magpie", icon: "magpie", kind: "relay", added: false,
    note: "another computer's magpie, shared on its network", endpoint: "http://192.168.1.20:3425",
    endpointHint: "The address and API key the other computer's magpie shows in Settings, under Share on local network. Its models and routing groups are listed here; each request goes on in the API the agent spoke.",
    endpointNeeded: "The other magpie's address is needed" },
];
const providers = [{ id: "openrouter", name: "OpenRouter", icon: "openai", preset: "openrouter", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang, saves) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/save") {
      saves.push(route.request().postDataJSON());
      return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
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
  en: { field: "Endpoint", hint: /Share on local network.*the API the agent spoke/, needed: "The other magpie's address is needed", note: "another computer's magpie, shared on its network", add: "Add" },
  zh: { field: "终结点", hint: /设置 → 局域网共享.*原样转发/, needed: "需要填写另一台 magpie 的地址", note: "另一台电脑上的 magpie，局域网共享", add: "添加" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Remote magpie asks for the other magpie's address", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const saves = [];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        // A remote's explicit list is authoritative, including an empty
        // list after refresh: Jev Router still belongs to chat.
        const classifications = await page.evaluate(() => {
          const remote = {
            preset: "remote-magpie", chat: "http://office/v1", decide: "http://office/v1",
            deciders: ["judge/custom", "judge/@cf/cloudflare/clef"],
          };
          return [
            decidesModel(remote, "judge/custom"),
            decidesModel(remote, "judge/@cf/cloudflare/clef"),
            decidesModel(remote, "relay/typesafe/jev-router"),
            decidesModel({ ...remote, deciders: [] }, "relay/typesafe/jev-router"),
          ];
        });
        assert.deepEqual(classifications, [true, true, false, false]);
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        const tile = sheet.locator(".tile", { has: page.locator(".n", { hasText: /^Remote magpie$/ }) });
        assert.equal(await tile.getAttribute("title"), w.note);
        await tile.click();
        const ed = page.locator(".editor.new");
        await ed.locator(".ehead b", { hasText: "Remote magpie" }).waitFor();

        // the field, above the key, focused first; the OpenAI preset has none
        const label = ed.locator("label", { hasText: new RegExp("^" + w.field + "$") });
        assert.equal(await label.count(), 1);
        const endpoint = ed.locator("input.endpoint");
        assert.equal(await endpoint.getAttribute("placeholder"), "http://192.168.1.20:3425");
        assert.equal(await endpoint.getAttribute("type"), "url");
        assert.match(await endpoint.locator("xpath=following-sibling::div[contains(@class,'hint')]").textContent(), w.hint);
        await page.waitForFunction(() => document.activeElement?.classList.contains("endpoint"));
        const key = ed.locator("input[type=password]").first();
        const [ey, ky] = [await endpoint.boundingBox(), await key.boundingBox()].map((b) => b.y);
        assert(ey < ky, "the endpoint above the key");

        // no endpoint: said, nothing sent, the page where it was
        await key.fill("sk-magpie-x");
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        assert.equal(await ed.locator(".editor-error").textContent(), w.needed);
        assert.equal(saves.length, 0);
        assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("endpoint")), true);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y);

        // with one: sent as the preset's, with the key
        await endpoint.fill("192.168.1.20:3425");
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        await page.waitForFunction(() => !document.querySelector(".editor.new"));
        assert.equal(saves.length, 1);
        const s = saves[0];
        assert.equal(s.preset, "remote-magpie");
        assert.equal(s.key, "sk-magpie-x");
        assert.equal(s.chat, "192.168.1.20:3425");

        // another preset's editor has no endpoint field
        await page.locator("#addProvider").click();
        await sheet.locator(".tile .n", { hasText: /^OpenAI$/ }).click();
        await ed.locator(".ehead b", { hasText: "OpenAI" }).waitFor();
        assert.equal(await ed.locator("input.endpoint").count(), 0);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
