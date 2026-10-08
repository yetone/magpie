// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's Requests and Accounts and keys, side by side (a report
// with a screenshot: "这里排版是不是有问题啊？感觉好乱"): the two lists end on one
// line, the accounts scrolling like the requests instead of running on down
// the page beside an empty column; the models an account answered sit on a
// line of their own, not wrapped in among its numbers; a provider's row
// says its model once, under the provider's name, not the name again and the
// model twice. One column, the accounts list is as long as it is. Side by
// side only where each request still fits one line; narrower, one column of
// one-line requests, not two with each request on three (#860).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const renew = new Date(now.getTime() + 3 * 864e5).toISOString();
const W = {
  aihub: { id: "aihubmix", provider: "aihubmix", name: "AiHubMix", kind: "provider", model: "glm-5.3-flash", icon: "aihubmix-color", routing: "smart" },
  c1: { id: "codex:a", provider: "codex", name: "Codex", who: "ann@example.com", plan: "plus", kind: "account", model: "gpt-5.6-terra", known: true, used: 9, renews: [renew], icon: "codex-color", routing: "smart" },
  c2: { id: "codex:b", provider: "codex", name: "Codex", who: "bob@example.com", plan: "pro", kind: "account", model: "gpt-5.6-terra", known: true, used: 4, renews: [renew], icon: "codex-color", routing: "smart" },
  cop: { id: "copilot:a", provider: "copilot", name: "Copilot", who: "ann", kind: "account", model: "gpt-6-sol", known: true, used: 1, renews: [renew], icon: "githubcopilot", routing: "smart" },
  ds: { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-flash", icon: "deepseek-color", routing: "smart" },
  or: { id: "openrouter", provider: "openrouter", name: "OpenRouter", kind: "provider", model: "openai/gpt-6-sol", icon: "openrouter", routing: "smart" },
  wb: { id: "wb:a", provider: "workbuddy", name: "WorkBuddy", who: "ann", kind: "account", model: "glm", known: false, icon: "generic", routing: "smart" },
};
const mk = (i, w, model, extra = {}) => ({
  id: 200 - i, seq: 200 - i, time: at(i), agent: i % 2 ? "codex" : "curl", model, provider: w.provider,
  order: Object.values(W).filter((x) => x.provider === w.provider), done: true, status: 200, ms: 5000 + i * 900, tokens: 90000 + i * 700,
  tries: [{ id: w.id, model: w.model || model.split("/").pop(), start: at(i), done: true, status: 200, ms: 5000 }], ...extra,
});
const routes = [
  mk(0, W.ds, "deepseek/deepseek-flash"), mk(1, W.ds, "deepseek/deepseek-flash"),
  ...[2, 3, 4, 5].map((i) => ({ ...mk(i, W.c1, "codex/gpt-5.6-terra"), tries: [{ id: W.c1.id, model: "gpt-5.6-terra", start: at(i), done: true, status: 200, ms: 8000, effort: "medium" }] })),
  { ...mk(6, W.c1, "codex/gpt-6-luna"), tries: [{ id: W.c1.id, model: "gpt-6-luna", start: at(6), done: true, status: 200, ms: 8000 }] },
  { ...mk(7, W.c1, "codex/gpt-5.6-luna"), tries: [{ id: W.c1.id, model: "gpt-5.6-luna", start: at(7), done: true, status: 200, ms: 8000 }] },
  mk(8, W.cop, "copilot/gpt-6-sol"), mk(9, W.aihub, "aihubmix/glm-5.3-flash"), mk(10, W.or, "openrouter/openai/gpt-6-sol"), mk(11, W.wb, "workbuddy/glm"),
];
function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const assets = path.resolve(__dirname, "../assets");
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 200, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: requests and accounts side by side`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1440, height: 1400 } });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator(".rt-cols").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-columns.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-act").first().waitFor();
      await page.waitForTimeout(300);

      const box = (s) => page.locator(s).evaluate((e) => { const b = e.getBoundingClientRect(); return { left: b.left, right: b.right, bottom: b.bottom, scroll: e.scrollHeight > e.clientHeight + 1 }; });
      const reqs = await box(".rt-reqs"), acts = await box(".rt-acts");
      assert(acts.left >= reqs.right, "side by side");
      assert(Math.abs(acts.bottom - reqs.bottom) <= 1, `the lists end on one line: ${reqs.bottom} / ${acts.bottom}`);
      assert(acts.scroll, "the rest of the accounts scroll");
      // a request on one line: its destination and numbers beside its model
      const oneLine = () => page.locator(".rt-req").evaluateAll((rs) => rs.every((r) => {
        const y = (s) => { const b = r.querySelector(s).getBoundingClientRect(); return b.width ? b.top : null; };
        const a = y(".asked");
        return [y(".to"), y(".meta")].every((v) => v === null || Math.abs(v - a) < 4);
      }));
      for (const width of [940, 1100]) {
        await page.setViewportSize({ width, height: 1400 });
        await page.waitForTimeout(200);
        const requests = await box(".rt-reqs"), accounts = await box(".rt-acts");
        assert(accounts.left < requests.right, `${width}px: one column`);
        assert(await oneLine(), `${width}px: each request on one line`);
      }
      for (const width of [1440, 1920]) {
        await page.setViewportSize({ width, height: 1400 });
        await page.waitForTimeout(200);
        const requests = await box(".rt-col:first-child"), accounts = await box(".rt-col:last-child");
        const rw = requests.right - requests.left, aw = accounts.right - accounts.left;
        assert(rw > aw * 1.6, `${width}px: requests should have more room than accounts (${rw} / ${aw})`);
        assert(aw >= 280 && aw <= 400.5, `${width}px: account column stays readable and capped (${aw})`);
        const rightList = await box(".rt-acts"), leftList = await box(".rt-reqs");
        assert(Math.abs(rightList.bottom - leftList.bottom) <= 1, `${width}px: list bottoms stay aligned`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${width}px: no horizontal overflow`);
        const clipped = await page.locator(".rt-act .mdls code").evaluateAll((es) => es.some((e) => e.getBoundingClientRect().right > e.closest(".rt-act").getBoundingClientRect().right + 1));
        assert.equal(clipped, false, `${width}px: models stay inside the narrower account column`);
        assert(await oneLine(), `${width}px: side by side, each request still on one line`);
      }

      // the Codex account's three models on their own line, none in the tally
      const codex = page.locator(".rt-act", { hasText: "ann@example.com" });
      assert.deepEqual((await codex.locator(".mdls code").allInnerTexts()).sort(), ["gpt-5.6-luna", "gpt-5.6-terra", "gpt-6-luna"]);
      assert.equal(await codex.locator(".tally code").count(), 0);
      const tally = await codex.locator(".tally").boundingBox(), mdls = await codex.locator(".mdls").boundingBox();
      assert(mdls.y >= tally.y + tally.height - 0.5, "the models under the numbers");

      // a provider's row: its model once, not its name again
      const ai = page.locator(".rt-act").first();
      assert.equal((await ai.locator(".nm").innerText()).trim(), "glm-5.3-flash");
      assert.equal(await ai.locator("code").count(), 0);

      // one column: the accounts as long as they are
      await page.setViewportSize({ width: 700, height: 1400 });
      await page.waitForTimeout(300);
      const one = await box(".rt-acts");
      assert(!one.scroll, "one column: nothing hidden in the accounts");
      assert.equal(await page.locator(".rt-acts").evaluate((e) => e.style.maxHeight), "");
      assert.deepEqual(errors, []);
    });
  }
}
