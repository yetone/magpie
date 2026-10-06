// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's sign-in has three ways: ChatGPT (the default), which says that
// magpie is Codex's provider while the ChatGPT account is used up, so the
// Codex app still sends; Always ChatGPT, which never does that and says
// what it costs; and magpie API. The square opens the app's picker with
// each way's sentence whole, in the page's language; Always ChatGPT is
// posted as "chatgpt" and lights the square, ChatGPT posts "" again. A
// click scrolls nothing. At 1100px and 440px, English, Chinese, Japanese
// and German, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "gpt-5.5", label: "GPT-5.5", ref: "openai/gpt-5.5" }];
// as internal/agent/codex.go has them
const ways = [
  { value: "", label: "ChatGPT", note: "magpie's models join Codex's own; Codex stays signed in to ChatGPT, and while its account is used up magpie is Codex's provider, so the Codex app still sends" },
  { value: "chatgpt", label: "Always ChatGPT", note: "as ChatGPT, and kept so when its account is used up: magpie never becomes Codex's provider. The Codex app may then send nothing till the account has room; Codex CLI goes on through magpie" },
  { value: "api", label: "magpie API", note: "magpie is Codex's provider; the Codex app is in its API state, with magpie's models only" },
];
const fresh = () => ({
  agents: [{
    id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
    fields: [
      { key: "model", label: "model", value: "gpt-5.5", options: models },
      { key: "login", label: "sign-in", value: "", options: ways },
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

const codex = '.row.agent[data-id="codex"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: Codex's sign-in can be kept on ChatGPT when its account is used up`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-codex-signin-always-${i}.png`) });
        }
        await browser.close();
      });
      for (const width of [1100, 440]) {
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        pages.push(page);
        page.setDefaultTimeout(5000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets));
        await page.goto("http://magpie.test/");
        await page.locator(codex).waitFor();
        const words = await page.evaluate(([ways, lang]) => ways.map((w) => lang === "en" ? [w.label, w.note] : [I18N[lang][w.label] || w.label, I18N[lang][w.note]]), [ways, lang]);
        assert(words.every(([, n]) => n), `every sentence has its ${lang}`);
        assert.equal(words[1][0] === "Always ChatGPT", lang === "en", `"Always ChatGPT" has its ${lang}`);
        await page.locator(`${codex} .ag-link`).click();
        await page.locator(`${codex} .ag-exp`).waitFor();
        const square = page.locator(`${codex} .field[data-key="login"]`).first();
        await square.scrollIntoViewIfNeeded();
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
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="codex"] .field.set[data-key="login"]'));
        assert.deepEqual(sets, [{ agent: "codex", field: "login", value: "chatgpt" }]);
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.locator("#pop #list li.cur .v").textContent(), words[1][0]);
        await way(0).click();
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .field.set[data-key="login"]'));
        assert.deepEqual(sets.at(-1), { agent: "codex", field: "login", value: "" });
        assert.deepEqual(errors, []);
      }
    });
  }
}
