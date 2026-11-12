// Run with Node's test runner and Playwright on the module path; see README.md.
// Long conversations, in Codex's opened row (its one row since the owner
// merged the two, 11-12): a model_context_window or
// model_auto_compact_token_limit in Codex's config.toml comes before it in
// Codex (zgjfjfl on Discord: 128000 there, 544K here, and every
// conversation compacted near 128K), so the row names each one, with the
// profile it is in, and Take it out posts settings/codex-compact-held/drop
// on its own; once they are gone the line is too. The model Codex runs
// is named with its window when Long conversations is over it (gpt-6-A at
// 128K), and stays. Nothing scrolls the page.
// Every language, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, compactAt: 544000, codexCompactKeys: keys, codexWindow: { model: "relay/gpt-6-A", window: 128000, n: "128K" } };
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/codex-compact-held/drop") {
      posts.push({ path: url.pathname, body: req.postDataJSON() });
      settings = { ...settings, codexCompactKeys: undefined };
      return json(settings);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") posts.push({ path: url.pathname, body: req.postDataJSON() });
      return json(settings);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const keys = "model_context_window = 128000, [profiles.work] model_auto_compact_token_limit = 120000";
const words = {
  en: { line: `Codex's config.toml sets ${keys}, read before Long conversations`, drop: "Take it out", at: "Compact at 544K", own: "Codex compacts relay/gpt-6-A at 90% of its 128K window, before Long conversations; set a larger one in its provider's Context window" },
  zh: { line: `Codex 的 config.toml 设了 ${keys}，Codex 先读它再读「长对话」，所以按它压缩`, drop: "拿掉它", at: "544K 时压缩", own: "magpie 告诉 Codex relay/gpt-6-A 的窗口是 128K，所以 Codex 在它的 90% 处压缩，不等「长对话」；更大的窗口在它服务商的「上下文窗口」里设" },
  "zh-TW": { line: `Codex 的 config.toml 設了 ${keys}，Codex 先讀它再讀「長對話」，所以按它壓縮`, drop: "拿掉它", at: "544K 時壓縮", own: "magpie 告訴 Codex relay/gpt-6-A 的視窗是 128K，所以 Codex 在它的 90% 處壓縮，不等「長對話」；更大的視窗在它服務商的「上下文視窗」裡設" },
  ja: { line: `Codex の config.toml に ${keys} があり、Codex は「長い会話」より先にこれを読むため、これで圧縮します`, drop: "取り除く", at: "544K で圧縮", own: "magpie は Codex に relay/gpt-6-A のウィンドウを 128K と伝えているため、Codex は「長い会話」より先にその 90% で圧縮します。より大きいウィンドウはプロバイダの「コンテキストウィンドウ」で設定します" },
  de: { line: `Codex’ config.toml setzt ${keys}, das Codex vor „Lange Unterhaltungen“ liest, also komprimiert es danach`, drop: "Entfernen", at: "Bei 544K komprimieren", own: "magpie nennt Codex für relay/gpt-6-A ein Fenster von 128K, also komprimiert Codex bei 90 % davon, vor „Lange Unterhaltungen“; ein größeres wird im „Kontextfenster“ seines Anbieters gesetzt" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: Long conversations names the config keys Codex reads first, and takes them out`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=agents");
        await openAgentSettings(page);
        // the window magpie tells Codex for the model its config runs, when Long conversations is over it (gpt-6-A)
        const own = page.locator(".agent-compact .compact-held").filter({ hasText: w.own });
        await own.waitFor();
        assert.equal((await own.textContent()).trim(), w.own);
        const note = page.locator(".agent-compact .compact-held").filter({ has: page.locator("button") });
        await note.waitFor();
        assert.equal((await note.locator("span").textContent()).trim(), w.line);
        if (w.at) await page.locator(".row.agent-compact").getByText(w.at, { exact: true }).waitFor();
        const box = await note.boundingBox(), row = await page.locator(".row.agent-compact").boundingBox();
        assert(box.x >= row.x - 1 && box.x + box.width <= row.x + row.width + 1, `the line stays in its row: ${JSON.stringify({ box, row })}`);
        const view = () => page.locator("#view-agents").evaluate((v) => v.scrollTop);
        // the button is wheeled into view first, as the reader would, so only the click itself is measured (en, ja and de at 420px)
        const drop = note.getByRole("button", { name: w.drop, exact: true });
        await reach(page, drop);
        const before = await view();
        await drop.click();
        await note.waitFor({ state: "detached" });
        assert.equal(await own.count(), 1, "the model's own window stays named once the keys are out");
        assert.deepEqual(posts.map((p) => [p.path, p.body]), [["/api/settings/codex-compact-held/drop", {}]]);
        // the page may only lose what the gone line took from its bottom: the browser holds scrollTop under the new end, to the pixel
        const [after, end] = await page.locator("#view-agents").evaluate((v) => [v.scrollTop, v.scrollHeight - v.clientHeight]);
        assert(Math.abs(after - Math.min(before, end)) <= 1, `the click scrolled the page: ${JSON.stringify({ before, after, end })}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
