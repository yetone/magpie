// Run with Node's test runner and Playwright on the module path; see README.md.
// Small context first (routes.SmallFirst, a routing group's; yg6888 on X)
// on the Routing page: a request's story says why the first went first in
// the words of its model's context window, as the gateway's trace gives it
// (Weighed.context): "Composer 2.5's 200K context window is the smallest
// that holds this request". One with no known window says it is next. The
// mode line says what Small context first does. In both engines and every
// language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const key = (who, more) => ({ id: who + "#k", provider: who, name: who, who, kind: "key", model: "composer-2.5", routing: "fit", ...more });

const scenes = {
  // the smallest window that holds it goes first, the 1M one after
  smallest: { order: [key("cursor", { context: 200000 }), key("codex", { context: 1000000 })] },
  // none with a known window: the listed first
  unknown: { order: [key("relay"), key("other")] },
};

function serve(lang, scene) {
  const s = scenes[scene];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "composer-2.5", provider: s.order[0].provider, done: true, status: 200, ms: 900, tokens: 1200, order: s.order,
    tries: [{ id: s.order[0].id, model: "composer-2.5", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, scene, width = 1100) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/*", serve(lang, scene));
  await page.goto("http://magpie.test/?view=routing");
  const req = page.locator(".rt-req").first();
  await req.waitFor();
  if (await req.getAttribute("aria-pressed") !== "true") await req.click();
  await page.locator(".rt-steps li.why").first().waitFor();
  // what a string says in lang, its {names} filled in; a language without
  // it would say it in English, which the i18n-keys test catches
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  return { page, errors, say, why: async () => (await page.locator(".rt-steps li.why").first().textContent()).trim() };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the smallest that holds it says its window`, async (t) => {
      const { page, errors, say, why } = await open(t, engine, lang, "smallest");
      assert.equal(await why(), await say("Small context first: {who}'s {n} context window is the smallest that holds this request.", { who: "cursor", n: "200K" }));
      assert.equal((await page.locator(".rt-mode").textContent()).trim(),
        await say("Small context first: each request goes to the member with the smallest context window that holds it, so the big ones are kept for long requests; one whose window it is longer than is left out."));
      if (lang !== "en") assert.notEqual(await say("Small context first"), "Small context first", `${lang} names Small context first`);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: no window known, the next`, async (t) => {
      const { errors, say, why } = await open(t, engine, lang, "unknown");
      assert.equal(await why(), await say("Small context first: no member with a known context window can take this request, so {who} is next.", { who: "relay" }));
      assert.deepEqual(errors, []);
    });
  }
}
