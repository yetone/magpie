// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code running through magpie signs in to it with magpie's key, or
// keeps its own claude.ai sign-in, which a key signs it out of. The way is
// a square in the row opened from its link, beside its subagents',
// ultracode and the subagents' effort, all of them inside the row at
// 440px. It opens the two ways, each said whole in the page's language;
// claude.ai is posted as "claudeai" and lights the square, magpie's key
// posts "" again. Claude Code on its own model has no other way to take,
// and no square. At 1100px and 440px, English, Chinese (both), Japanese and
// German, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "deepseek/deepseek-v4-pro", label: "DeepSeek V4 Pro", ref: "deepseek/deepseek-v4-pro" }, { value: "deepseek/deepseek-v4-flash", label: "DeepSeek V4 Flash", ref: "deepseek/deepseek-v4-flash" }];
const levels = ["low", "medium", "high", "xhigh", "max"].map((value) => ({ value }));
// as internal/agent/claude.go has them (claudeSignIns)
const ways = [
  { value: "", label: "magpie's key", note: "Claude Code sends magpie its key and is signed out of claude.ai while it runs through magpie: claude.ai's plan limits in /usage, its connectors, voice and /teleport are off" },
  { value: "claudeai", label: "claude.ai", note: "Claude Code keeps its claude.ai sign-in (/login), so those work; it sends that sign-in to magpie, which never passes it on. Remote Control and ultrareview stay off: Claude Code has them only on Anthropic's own address" },
];
const tiers = ["opus", "sonnet", "haiku", "fable"];
const fresh = () => ({
  agents: [{
    // every field Claude Code has through magpie
    id: "claude", name: "Claude Code", path: "/test/settings.json", icon: "claudecode-color", wired: true,
    fields: [
      { key: "model", label: "model", value: models[0].value, options: models },
      { key: "effort", label: "effort", value: "high", options: levels },
      { key: "ultracode", label: "ultracode", value: "", options: [{ value: "on", note: "Claude plans a workflow for each substantive task" }] },
      ...tiers.map((tier) => ({ key: tier, label: tier, value: "", options: models })),
      { key: "subagent", label: "subagents", value: "", options: models },
      ...tiers.map((tier) => ({ key: tier + "_effort", label: tier + " effort", value: "", options: levels })),
      { key: "subagent_effort", label: "subagent effort", value: "", options: levels },
      { key: "login", label: "sign-in", value: "", options: ways },
    ],
  }, {
    // on its own model: no way but its own
    id: "cc-own", name: "Claude Code", path: "/test/own/settings.json", icon: "claudecode-color", wired: false,
    fields: [
      { key: "model", label: "model", value: "opus", options: [{ value: "opus" }] },
      { key: "login", label: "sign-in", value: "", options: [] },
    ],
  }],
  profiles: [],
});

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const claude = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Claude Code through magpie can keep its claude.ai sign-in`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-signin-${i}.png`) });
        }
        await browser.close();
      });
      for (const width of [1100, 440]) {
        // opened at once, as claude-effort.test.cjs: Claude Code's row is
        // tall, its squares below the fold at 440px
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets));
        await page.goto("http://magpie.test/");
        await page.locator(claude).waitFor();
        const words = await page.evaluate(([ways, lang]) => ways.map((w) => lang === "en" ? [w.label, w.note] : [I18N[lang][w.label] || w.label, I18N[lang][w.note]]), [ways, lang]);
        assert(words.every(([, n]) => n), `every sentence has its ${lang}`);
        assert.equal(words[0][0] === "magpie's key", lang === "en", `"magpie's key" has its ${lang}`);
        assert.equal(words[1][0], "claude.ai");
        assert.equal(await page.locator('.row.agent[data-id="cc-own"] .field[data-key="login"]').count(), 0, "a sign-in square with no other way to take");

        await page.locator(`${claude} .ag-link`).click();
        await page.locator(`${claude} .ag-exp`).waitFor();
        const square = page.locator(`${claude} .field[data-key="login"]`).first();
        // down to it as a reader scrolls: the page puts back a scroll that
        // isn't the reader's, so the click itself has no need to scroll
        await page.mouse.move(width / 2, 400);
        for (let i = 0; i < 40; i++) {
          const fits = await square.evaluate((e) => { const r = e.getBoundingClientRect(), v = e.closest(".view").getBoundingClientRect(); return r.top >= v.top && r.bottom <= v.bottom - 8; });
          if (fits) break;
          await page.mouse.wheel(0, 40);
          await page.waitForTimeout(20);
        }
        await page.waitForTimeout(300);
        const label = await square.getAttribute("aria-label");
        assert(label.includes(words[0][0]) && label.includes(words[0][1]), label);
        assert.equal(await square.evaluate((e) => e.classList.contains("set")), false);
        // the row's squares stay inside it, and the page doesn't scroll sideways
        const fit = await page.locator(claude).evaluate((row) => {
          const r = row.getBoundingClientRect();
          return { out: [...row.querySelectorAll(".extras-cell .field")].map((e) => e.getBoundingClientRect()).filter((b) => b.left < r.left - 0.5 || b.right > r.right + 0.5).length, wide: document.documentElement.scrollWidth > innerWidth };
        });
        assert.deepEqual(fit, { out: 0, wide: false }, `the row's squares fit at ${width}px`);

        const y = await page.evaluate(() => [scrollY, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);
        await square.click();
        await page.locator("#pop.explained:not([hidden]) #list li").first().waitFor();
        assert.deepEqual(await page.evaluate(() => [scrollY, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]), y, "the click scrolled the page");
        const items = await page.locator("#pop #list li").evaluateAll((ls) => ls.map((l) => {
          const v = l.querySelector(".v"), n = l.querySelector(".n"), pop = document.querySelector("#pop").getBoundingClientRect(), r = l.getBoundingClientRect();
          return { v: v.textContent, n: n?.textContent, whole: n ? n.scrollWidth <= n.clientWidth + 1 && n.scrollHeight <= n.clientHeight + 1 : false, inside: r.left >= pop.left - 0.5 && r.right <= pop.right + 0.5 };
        }));
        assert.deepEqual(items.map((i) => [i.v, i.n]), words);
        assert(items.every((i) => i.whole && i.inside), JSON.stringify(items));
        const way = (i) => page.locator("#pop #list li").filter({ has: page.locator(".n", { hasText: words[i][1] }) });
        await way(1).click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude"] .field.set[data-key="login"]'));
        assert.deepEqual(sets, [{ agent: "claude", field: "login", value: "claudeai" }]);
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.locator("#pop #list li.cur .v").textContent(), "claude.ai");
        await way(0).click();
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="claude"] .field.set[data-key="login"]'));
        assert.deepEqual(sets.at(-1), { agent: "claude", field: "login", value: "" });
        assert.deepEqual(errors, []);
      }
    });
  }
}
