// Run with Node's test runner and Playwright on the module path; see README.md.
// #1929 (versun): a group whose classifier couldn't say the last time it was
// asked — MLX-Serve turned its decision model away as a chat model with a
// 400 — says so on its Routing card, with why and when, rather than only in
// each request's details while its rules with an intent quietly never
// matched. A dot marks it, never a coloured stripe. A group whose classifier
// answered says nothing. Both engines, 1100 and 420 wide, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const why = "mlx-serve/clef-flash-4bit: This is a typed-decision model; it cannot serve chat/text requests. Use POST /v1/decisions instead.";
const models = [
  { id: "mlx-serve/qwen", name: "qwen", providerName: "MLX-Serve", icon: "generic" },
  { id: "or/b", name: "b", providerName: "OpenRouter", icon: "generic" },
];
const members = ["mlx-serve/qwen", "or/b"];
const group = (id, name, extra) => ({ id, name, members, fast: [], off: [], routing: "order", ready: true, patterns: [],
  memberInfo: members.map((m) => ({ id: m, ready: true })), rules: [{ use: "or/b", intent: "planning" }], ...extra });
const groups = () => ({
  models, pools: [], deciders: [{ id: "mlx-serve/clef-flash-4bit", name: "clef-flash-4bit", providerName: "MLX-Serve", icon: "generic" }],
  groups: [
    group("local", "Local", { classifier: "mlx-serve/clef-flash-4bit", classifierFailed: { error: why, at: new Date(Date.now() - 3 * 60000).toISOString() } }),
    group("fine", "Fine", { classifier: "or/b" }),
  ],
});
const prefix = { en: "Classifier failed", zh: "分类器", "zh-TW": "分類器", ja: "分類器が", de: "Klassifizierer fehlgeschlagen" };

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a group's card says its classifier failed`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator(".rt-group[data-id=local]");
        await card.waitFor();
        const line = card.locator(".mem.clsfail");
        assert.equal(await line.count(), 1, "the failing classifier's group says so");
        const text = (await line.textContent()).trim();
        assert(text.startsWith(prefix[lang]), `in ${lang}: ${text}`);
        assert(text.includes(why), "with why");
        assert(!/\{(when|error)\}/.test(text), "every slot filled");
        const title = await line.getAttribute("title");
        assert(title.includes("mlx-serve/clef-flash-4bit") && title.includes(why) && !/\{\w+\}/.test(title), title);
        assert.equal(await page.locator(".rt-group[data-id=fine] .mem.clsfail").count(), 0, "a classifier that answered says nothing");
        const look = await line.evaluate((n) => {
          const s = getComputedStyle(n), d = getComputedStyle(n.querySelector(".dot")), r = n.getBoundingClientRect(), c = n.closest(".rt-group").getBoundingClientRect();
          return { stripe: parseFloat(s.borderLeftWidth), dot: d.borderRadius, inside: r.right <= c.right + 0.5 && r.left >= c.left - 0.5, h: r.height };
        });
        assert.equal(look.stripe, 0, "no left-border stripe");
        assert.equal(look.dot, "50%", "a dot marks it");
        assert(look.inside, "the line stays in its card");
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `no sideways scroll: ${over}`);
        if (lang !== "en") {
          const missing = await page.evaluate((lang) => ["Classifier failed {when}: {error}",
            "{by} couldn't say which intent or effort a turn is the last time it was asked: {error}. Until it answers, no rule with an intent matches and turns reason as the agent asked. Each request's details say what it was asked.",
          ].filter((k) => !I18N[lang][k]), lang);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
