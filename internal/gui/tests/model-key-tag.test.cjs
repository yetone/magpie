// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider whose keys each fetch their own models (cliddO11 on X: "when
// several keys each fetch models, could a colour or a small tag mark which
// key each model belongs to?") says on a model's chip, quietly after its
// name, which keys list it when not all of them do: one key's name (its
// masked value when it has none), two keys' names, or how many past two,
// the whole list in the tooltip. A model every key lists, and every model
// of a provider whose list isn't asked per key, carries no tag. The tag is
// grey (the 1M tag's look), never the accent, and stays inside its chip at
// a phone's width. Chromium and WebKit, wide and narrow, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSET_DIR || path.resolve(__dirname, "../assets"));
const fetched = new Date(Date.now() - 3600e3).toISOString();
const key = (id, name, active) => ({ id, name, masked: "sk-…" + id.slice(0, 4), on: true, ...(active ? { active: true } : {}) });
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [
    { id: "gpt-5.5", name: "", on: true },
    { id: "claude-opus-5", name: "", on: true, keys: ["aaaaaaaaaa"] },
    { id: "glm-5.2", name: "", on: true, keys: ["bbbbbbbbbb"] },
    { id: "kimi-k3", name: "", on: true, keys: ["aaaaaaaaaa", "bbbbbbbbbb"] },
    { id: "deepseek-v4.1", name: "", on: true, keys: ["aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"], keyCount: 3 },
  ],
  chosen: [], fetched, agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-…aaaa" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [key("aaaaaaaaaa", "Team", true), key("bbbbbbbbbb", ""), key("cccccccccc", "Spare"), key("dddddddddd", "Night")],
};
// one key: its models are never tagged
const single = { ...relay, id: "single", name: "Single", chat: "https://single.example.com/v1", keyList: [], models: [{ id: "gpt-5.5", name: "", on: true }] };

function serve(lang) {
  const providers = { providers: [relay, single], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const cases = [...LANGS.map((lang) => [lang, 1100]), ["en", 420], ["zh", 420]];
const keysWord = { en: "3 keys", zh: "3 个密钥", "zh-TW": "3 個金鑰", ja: "3 個のキー", de: "3 Schlüssel" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of cases) {
    test(`${engine} ${lang} ${width}px: a model says which keys list it when not all do`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-key-tag.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");

      const editor = page.locator("#modal:not([hidden]) .editor");
      await page.locator(".row.provider", { hasText: "Relay" }).click();
      const chips = editor.locator(".mchips .mchip");
      await chips.first().waitFor();
      const drawn = await chips.evaluateAll((cs) => cs.map((c) => {
        const tag = c.querySelector(".mkey");
        return [c.querySelector("span").textContent, tag?.textContent ?? null, tag?.title ?? null];
      }));
      const only = (keys) => ({ en: `Listed by ${keys} only`, zh: `只有 ${keys} 列出这个模型`, "zh-TW": `只有 ${keys} 列出這個模型`, ja: `このモデルを一覧に持つのは ${keys} だけです`, de: `Nur ${keys} führt dieses Modell` })[lang];
      assert.deepEqual(drawn, [
        ["gpt-5.5", null, null],
        ["claude-opus-5", "Team", only("Team")],
        // a key with no name goes by its masked value, as its row does
        ["glm-5.2", "sk-…bbbb", only("sk-…bbbb")],
        ["kimi-k3", "Team · sk-…bbbb", only("Team, sk-…bbbb")],
        ["deepseek-v4.1", keysWord[lang], only("Team, sk-…bbbb, Spare")],
      ]);

      // quiet: grey, as the 1M tag, never the accent; the key's name as written
      const look = await editor.locator(".mkey").first().evaluate((e) => {
        const s = getComputedStyle(e), probe = document.createElement("span");
        probe.className = "badge ctx";
        e.parentElement.append(probe);
        const ctx = getComputedStyle(probe);
        const out = { color: s.color, bg: s.backgroundColor, ctxColor: ctx.color, ctxBg: ctx.backgroundColor, transform: s.textTransform, border: s.borderLeftWidth };
        probe.remove();
        return out;
      });
      assert.equal(look.color, look.ctxColor);
      assert.equal(look.bg, look.ctxBg);
      assert.equal(look.transform, "none");
      assert.equal(look.border, "0px");

      // each tag inside its chip, each chip inside the editor, narrow too
      const ed = await editor.boundingBox();
      const n = await chips.count();
      for (let i = 0; i < n; i++) {
        const c = await chips.nth(i).boundingBox();
        assert.ok(c.x >= ed.x - 0.5 && c.x + c.width <= ed.x + ed.width + 0.5, `chip ${i} ${JSON.stringify(c)} inside editor ${JSON.stringify(ed)}`);
        const tag = chips.nth(i).locator(".mkey");
        if (!await tag.count()) continue;
        const b = await tag.boundingBox();
        assert.ok(b.x >= c.x && b.x + b.width <= c.x + c.width + 0.5 && b.y >= c.y - 0.5 && b.y + b.height <= c.y + c.height + 0.5, `tag ${JSON.stringify(b)} inside chip ${JSON.stringify(c)}`);
      }

      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await editor.locator(".mchips").first().screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-key-tag-chips.png`) });
      }

      // a provider of one key: no tags
      await page.keyboard.press("Escape");
      await editor.waitFor({ state: "detached" }).catch(() => {});
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Single" }).click();
      await chips.first().waitFor();
      assert.equal(await editor.locator(".mkey").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
