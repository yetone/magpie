// Run with Node's test runner and Playwright on the module path; see README.md.
// An added preset's name is the user's to change, as a custom provider's is
// (yapechan on Discord: Kimi and Kimi (China) side by side, hard to tell
// apart). The editor has a Name box holding the preset's name as its
// placeholder, a name typed there is what Save sends, and a box left empty
// sends the preset's name back. A custom provider keeps its one Name box, and
// a subscription has none. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const row = (id, name, preset, host) => ({
  id, name, icon: "", preset, host, chat: `https://${host}/v1`, responses: "", anthropic: "", catalog: "",
  website: "", keysUrl: "", proxy: "", balanceToken: { takes: false, set: false },
  key: { set: true, masked: "sk-…abcd", optional: false }, ready: true, chosen: [], fallback: [], routing: "", affinity: "",
  models: [{ id: "kimi-k2", name: "kimi-k2", efforts: [], given: true, images: false, on: true }],
  exposed: 1, unlisted: false, off: false, fetched: "just now", agents: [], sponsored: false, keyList: [],
});
const preset = (id, name, host) => ({ id, name, icon: "", kind: "api", chat: `https://${host}/v1`, website: `https://${host}`, added: true });
const providers = {
  providers: [row("moonshot", "Kimi", "moonshot", "api.moonshot.ai"), row("moonshot-cn", "Kimi (China)", "moonshot-cn", "api.moonshot.cn"),
    row("my-relay", "My Relay", "", "relay.example")],
  presets: [preset("moonshot", "Kimi", "api.moonshot.ai"), preset("moonshot-cn", "Kimi (China)", "api.moonshot.cn")],
  excluded: [], gateway: { running: true, window: true },
};

function serve(lang, saved) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/provider/save") { saved.push(route.request().postDataJSON()); return json(providers); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = { en: { name: "Name", save: "Save", cancel: "Cancel" }, zh: { name: "名称", save: "保存", cancel: "取消" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: an added preset is renamed in its editor`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], saved = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, saved));
      await page.goto("http://magpie.test/?view=providers");
      const open = async (name) => {
        await page.locator(".row.provider", { has: page.locator(".name", { hasText: new RegExp(`^${name.replace(/[()]/g, "\\$&")}$`) }) }).click();
        const ed = page.locator(".editor");
        await ed.waitFor();
        return ed;
      };
      const nameBox = (ed) => ed.locator("label", { hasText: new RegExp(`^${w.name}$`) });

      // renamed: what Save sends is the name typed
      let ed = await open("Kimi (China)");
      assert.equal(await nameBox(ed).count(), 1, "one Name box");
      const box = ed.locator("input.provider-name");
      assert.equal(await box.inputValue(), "");
      assert.equal(await box.getAttribute("placeholder"), "Kimi (China)");
      await box.fill("Kimi 国内");
      await ed.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".editor"));
      assert.equal(saved.at(-1)?.name, "Kimi 国内");
      assert.equal(saved.at(-1)?.id, "moonshot-cn", "the id is kept");

      // emptied: the preset's name again
      ed = await open("Kimi");
      await ed.locator("input.provider-name").fill("x");
      await ed.locator("input.provider-name").fill("");
      await ed.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".editor"));
      assert.equal(saved.at(-1)?.name, "Kimi");

      // a custom provider keeps its own single Name box
      ed = await open("My Relay");
      assert.equal(await nameBox(ed).count(), 1, "custom: one Name box");
      assert.equal(await ed.locator("input.provider-name").count(), 0);
      await ed.locator(".bar .ed-cancel").click();
      await ed.waitFor({ state: "detached" });
      assert.deepEqual(errors, [], "nothing throws");
    });
  }
}
