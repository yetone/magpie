// Run with Node's test runner and Playwright on the module path; see README.md.
// #709: Pi has no default model of its own — Default in its model picker
// clears the default it has, and Pi then takes the first provider signed in
// (openai's gpt-5.5 before openai-codex's), so "what Pi ships with" read as
// a model Pi would keep. Pi's and OmO's Default says it clears the default
// and Pi picks one itself; other agents' says what it said. Pi's
// openai-codex models are rows of their own beside openai's of the same
// name. Both connected to magpie, so the pickers in their rows list every
// choice there is. In English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const piOptions = [
  { value: "openai/gpt-6-astra", note: "GPT-6 Astra", group: "OpenAI" },
  { value: "openai-codex/gpt-6-astra", note: "GPT-6 Astra", group: "OpenAI Codex" },
  { value: "openai-codex/gpt-5.5", note: "GPT-5.5", group: "OpenAI Codex" },
  { value: "magpie/relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" },
];
const state = {
  agents: [
    { id: "pi", name: "Pi", icon: "generic", path: "/fixture/pi", wired: true, fields: [{ key: "model", label: "model", value: "openai/gpt-6-astra", options: piOptions }] },
    { id: "claude", name: "Claude Code", icon: "generic", path: "/fixture/claude", wired: true, fields: [{ key: "model", label: "model", value: "opus", options: [{ value: "opus" }, { value: "sonnet" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }] }] },
  ],
  profiles: [],
};

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...state, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { pi: "clears the default model; Pi picks one on its own", claude: "what Claude Code ships with" },
  zh: { pi: "清除显式默认模型，由 Pi 自动选择", claude: "Claude Code 自带的默认值" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Pi's Default says Pi picks the model itself`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 620 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(row("pi")).waitFor();
      assert.ok(await page.evaluate(() => !!I18N.zh["clears the default model; {agent} picks one on its own"]), "the string has its Chinese");

      const open = async (id) => {
        await page.locator(`${row(id)} .field[data-key="model"]`).click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      };
      const close = async () => { await page.keyboard.press("Escape"); await page.locator("#pop").waitFor({ state: "hidden" }); };

      await open("pi");
      assert.equal(await page.locator("#list li", { hasText: w.pi }).count(), 1, "Pi's Default says it clears the default");
      assert.equal(await page.locator("#list li", { hasText: w.claude.replace("Claude Code", "Pi") }).count(), 0);
      const texts = await page.locator("#list li").allTextContents();
      assert.ok(texts.some((s) => s.includes("openai-codex/gpt-6-astra") || s.includes("gpt-6-astra")), texts.join(" | "));
      assert.equal(await page.locator("#list li", { hasText: "gpt-5.5" }).count(), 1, "openai-codex's own model is a row");
      const values = await page.evaluate(() => pick.options.map((o) => o.value));
      assert.ok(values.includes("openai/gpt-6-astra") && values.includes("openai-codex/gpt-6-astra"), "same-named models stay a row each: " + values);
      await close();

      await open("claude");
      assert.equal(await page.locator("#list li", { hasText: w.claude }).count(), 1, "other agents' Default is as it was");
      assert.equal(await page.locator("#list li", { hasText: w.pi.replace("Pi", "Claude Code") }).count(), 0);
      await close();
      assert.deepEqual(errors, []);
    });
  }
}
