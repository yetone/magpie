// Run with Node's test runner and Playwright on the module path; see README.md.
// A Remote magpie reached more than one way (evjohnicu on X: the home
// network's address, Tailscale's and EasyTier's): its editor has Other
// addresses under Advanced, one per line, shown as the backend has them
// and saved as typed; a new one's too. Another provider's editor has no
// such row. Every language, both engines, at a narrow width with no
// sideways scroll; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const presets = [
  { id: "openrouter", name: "OpenRouter", icon: "openai", kind: "relay", chat: "https://openrouter.ai/api/v1", added: true },
  { id: "remote-magpie", name: "Remote magpie", icon: "magpie", kind: "remote", added: false, endpoint: "http://192.168.1.20:3425" },
];
const providers = () => [
  { id: "openrouter", name: "OpenRouter", icon: "openai", preset: "openrouter", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } },
  { id: "office", name: "Office", icon: "magpie", preset: "remote-magpie", chat: "http://192.168.1.20:3425/v1", responses: "http://192.168.1.20:3425/v1", anthropic: "http://192.168.1.20:3425", decide: "http://192.168.1.20:3425/v1",
    takesResolve: true, addresses: ["http://100.101.2.3:3425"], models: [{ id: "lib/m1", name: "Chat", on: true }], agents: [], key: { set: true, masked: "sk-…ab12" } },
];

function server(lang, saves) {
  const list = providers();
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: list, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/save") {
      saves.push(route.request().postDataJSON());
      return json({ providers: list, presets, excluded: [], gateway: { running: true, window: true } });
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
  en: { label: "Other addresses", hint: /Tailscale or EasyTier/ },
  zh: { label: "其他地址", hint: /Tailscale、EasyTier/ },
  "zh-TW": { label: "其他位址", hint: /Tailscale、EasyTier/ },
  ja: { label: "ほかのアドレス", hint: /Tailscale や EasyTier/ },
  de: { label: "Weitere Adressen", hint: /Tailscale oder EasyTier/ },
};

// the row's label and hint, and whether it sits in Advanced
const rowOf = (ed) => ed.evaluate((e) => {
  const box = e.querySelector("textarea.other-addresses");
  if (!box) return null;
  return { value: box.value, label: box.parentElement.previousElementSibling?.textContent || "", hint: box.nextElementSibling?.textContent || "", advanced: !!box.closest("details.advanced") };
});

const openAdvanced = async (ed) => {
  const adv = ed.locator("details.advanced");
  if (!(await adv.evaluate((d) => d.open))) await adv.locator("summary").click();
  await ed.locator("textarea.other-addresses").waitFor();
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a Remote magpie's other addresses", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      await t.test(lang, async () => {
        const w = L[lang];
        const saves = [];
        const page = await (await browser.newContext({ viewport: { width: 420, height: 900 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saves));
        await page.goto("http://magpie.test/?view=providers");

        // a saved one: the address it has, under Advanced, saved as typed
        await page.locator('#providers .row[data-id="office"]').click();
        const ed = page.locator(".editor");
        await ed.locator(".ehead b", { hasText: "Office" }).waitFor();
        await openAdvanced(ed);
        const row = await rowOf(ed);
        assert.equal(row.value, "http://100.101.2.3:3425");
        assert.equal(row.advanced, true, "under Advanced");
        assert.equal(row.label, w.label);
        assert.match(row.hint, w.hint);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        await ed.locator("textarea.other-addresses").fill("http://100.101.2.3:3425\n10.144.0.2:3425\n");
        await Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/provider/save")), ed.locator(".bar button.primary").first().click()]);
        assert.equal(saves.length, 1);
        assert.deepEqual([saves[0].from, saves[0].addresses], ["office", ["http://100.101.2.3:3425", "10.144.0.2:3425"]]);
        await page.waitForFunction(() => !document.querySelector(".editor"));

        // emptied: sent empty, so the backend clears them
        await page.locator('#providers .row[data-id="office"]').click();
        await ed.locator(".ehead b", { hasText: "Office" }).waitFor();
        await openAdvanced(ed);
        await ed.locator("textarea.other-addresses").fill("");
        await Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/provider/save")), ed.locator(".bar button.primary").first().click()]);
        assert.deepEqual(saves[1].addresses, []);
        await page.waitForFunction(() => !document.querySelector(".editor"));

        // another provider's editor has none
        await page.locator('#providers .row[data-id="openrouter"]').click();
        await ed.locator(".ehead b", { hasText: "OpenRouter" }).waitFor();
        await ed.locator("details.advanced summary").click();
        await page.waitForTimeout(300); // the rows' import
        assert.equal(await ed.locator("textarea.other-addresses").count(), 0, "only a Remote magpie has other addresses");
        await page.keyboard.press("Escape");
        await ed.waitFor({ state: "detached" });

        // a new one: empty, and sent with the address
        await page.locator("#addProvider").click();
        // found by name, so the tile is in view (a scroll from code is put
        // back by the page's scroll guard)
        await page.locator("#addSheet input").first().fill("Remote magpie");
        await page.waitForFunction(() => document.querySelectorAll("#addSheet .tile").length === 1);
        await page.locator("#addSheet .tile .n", { hasText: /^Remote magpie$/ }).click();
        const add = page.locator(".editor.new");
        await add.locator(".ehead b", { hasText: "Remote magpie" }).waitFor();
        await add.locator("input.endpoint").fill("192.168.1.20:3425");
        await add.locator("input[type=password]").first().fill("sk-magpie-x");
        await openAdvanced(add);
        assert.equal((await rowOf(add)).value, "");
        await add.locator("textarea.other-addresses").fill("http://office.tail1234.ts.net:3425");
        await add.locator(".bar button.primary").first().click();
        await page.waitForFunction(() => !document.querySelector(".editor.new"));
        const s = saves.at(-1);
        assert.deepEqual([s.preset, s.chat, s.addresses], ["remote-magpie", "192.168.1.20:3425", ["http://office.tail1234.ts.net:3425"]]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
