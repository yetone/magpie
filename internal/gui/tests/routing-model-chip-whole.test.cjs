// Run with Node's test runner and Playwright on the module path; see README.md.
// A row on the Routing page's stage names its account, its plan and the
// model in a chip, then says what it is doing. The row answering a request
// smart routing picked by intent says "answering… · picked for “…”", and
// the intent is the decision model's own words, of any length: the status
// took the room and the chip was cut to "gpt-6.1-te…", so which model
// answered didn't show (yi_time on Discord: one Codex account, plan
// prolite, a group of its models). The chip is whole on every row kind
// (answering, gone on from, failed to the agent, resting, a plain
// provider's row, a fold of keys), and what is cut short is the status,
// read whole over it, or the name. Every language, 1600 and 1280 wide (the
// status beside the name, as the reporter had it) and 420 (under it);
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
// the reporter's intent, as far as their screenshot shows it, and on
const intent = "任务主要依靠视觉或界面判断，需要先读懂截图、设计稿和页面布局再动手修改样式与交互";
const who = "someone.with.a.long.address@example-company.test";
const acct = (model, fixed, extra) => ({ id: "codex-1", provider: "codex", name: "Codex", who, plan: "prolite", kind: "account", icon: "generic", model, ...(fixed ? { fixed } : {}), known: true, used: 30, ...extra });
const members = ["codex/gpt-6.1-terra", "codex/gpt-6.1-sol:medium", "codex/gpt-6-astra:high", "codex/gpt-6-luna:low", "openrouter/vendor/a-model-with-a-long-name-v2"];

// how: "live" (answering, one gone on from, one resting), "failed" (the
// last try failed to the agent), "fold" (three keys of one model, one answering)
function routeOf(how) {
  const t0 = Date.now() - 1500;
  const rule = { n: 1, use: "codex/gpt-6.1-terra", when: [`intent "${intent}"`], turn: 3, tokens: 9000,
    classified: { by: "c/cls", intents: [intent], intent, ms: 300 } };
  const group = { id: "main", name: "Main", members };
  const base = { id: 7, seq: 7, time: iso(t0), agent: "codex", model: "main", provider: "codex", tokens: 9000, group, rule };
  if (how === "fold") {
    const key = (n) => ({ id: "relay-" + n, provider: "relay", name: "Relay", who: "key-" + n, kind: "key", icon: "generic", model: "gpt-6.1-terra-preview-long-0611" });
    return { ...base, provider: "relay", group: { id: "main", name: "Main", members: ["relay/gpt-6.1-terra-preview-long-0611"] },
      rule: { ...rule, use: "relay/gpt-6.1-terra-preview-long-0611" },
      order: [key(1), key(2), key(3)], tries: [{ id: "relay-1", model: "gpt-6.1-terra-preview-long-0611", start: iso(t0 + 100) }], done: false };
  }
  const order = [acct("gpt-6.1-terra"), acct("gpt-6.1-sol", "medium"),
    acct("gpt-6-astra", "high", { rest: { why: "rate", status: 429, model: "gpt-6-astra", until: iso(Date.now() + 3600e3) } }),
    acct("gpt-6-luna", "low"),
    { id: "openrouter", provider: "openrouter", name: "OpenRouter", kind: "provider", icon: "generic", model: "vendor/a-model-with-a-long-name-v2" }];
  if (how === "failed") {
    return { ...base, order, done: true, status: 504, error: "no first token", ms: 1200,
      tries: [{ id: "codex-1", model: "gpt-6.1-sol", fixed: "medium", start: iso(t0), done: true, status: 429, fail: "rate", ms: 200 },
        { id: "codex-1", model: "gpt-6.1-terra", start: iso(t0 + 300), done: true, status: 504, fail: "slow", ms: 1200, error: "no first token" }] };
  }
  return { ...base, order, done: false,
    tries: [{ id: "codex-1", model: "gpt-6.1-sol", fixed: "medium", start: iso(t0), done: true, status: 429, fail: "rate", ms: 200 },
      { id: "codex-1", model: "gpt-6.1-terra", start: iso(t0 + 300) }] };
}

function serve(lang, route) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data }).catch(() => {});
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const routes = first ? [route] : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 1, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// every row's head and status, as laid out: where the chip is against the
// head that clips it, and whether the status is cut short
const rowsOf = (page) => page.evaluate(() => [...document.querySelectorAll(".rt-accts li:not(.rt-gap):not(.rt-sub)")].map((li) => {
  const b = li.querySelector(":scope > b"), chip = b?.querySelector("code.mdl"), em = li.querySelector(":scope > em");
  const r = (e) => e.getBoundingClientRect();
  return {
    fold: li.classList.contains("rt-fold"), chip: chip?.textContent || "",
    chipIn: chip ? r(chip).left >= r(b).left - 0.5 && r(chip).right <= r(b).right + 0.5 : false,
    chipW: chip ? r(chip).width : 0, chipText: chip ? chip.scrollWidth - chip.clientWidth : 0,
    em: em?.textContent || "", emW: em ? r(em).width : 0, emCut: em ? em.scrollWidth > em.clientWidth + 1 : false, tip: em?.title || "",
  };
}));

const words = (page, lang) => page.evaluate(([lang, intent]) => {
  const t = (k) => (lang !== "en" && I18N[lang]?.[k]) || k;
  return { answering: t("answering…"), picked: t("picked for “{intent}”").replace("{intent}", intent) };
}, [lang, intent]);

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of engines) for (const lang of langs) for (const width of [1600, 1280, 420]) for (const how of ["live", "failed", "fold"]) {
  test(`${engine} ${lang} ${width}px ${how}: a row's model chip is whole; its status is what is cut short`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width, height: 1000 } })).newPage();
    page.setDefaultTimeout(8000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve(lang, routeOf(how)));
    await page.goto("http://magpie.test/?view=routing");
    const w = await words(page, lang);
    const want = `${w.answering} · ${w.picked}`;
    if (how === "fold") await page.waitForFunction((want) => [...document.querySelectorAll(".rt-accts li.rt-fold > em")].some((e) => e.textContent.includes(want)), want);
    else if (how === "live") await page.waitForFunction((want) => [...document.querySelectorAll(".rt-accts li > em")].some((e) => e.textContent === want), want);
    else await page.waitForFunction(() => document.querySelector(".rt-accts li.rest") && document.querySelectorAll(".rt-accts li > b code.mdl").length >= 5);
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    const rows = await rowsOf(page);
    // the list is laid out wide, the status beside the name, as the
    // reporter's was; narrow, the status goes under the name
    const beside = await page.evaluate(() => !document.querySelector(".rt-accts").classList.contains("max560"));
    assert.equal(beside, width > 560, `the list's layout at ${width}px`);
    assert(rows.length >= (how === "fold" ? 1 : 5), `rows: ${JSON.stringify(rows)}`);
    for (const r of rows) {
      assert(r.chip, `every row names its model: ${JSON.stringify(r)}`);
      assert(r.chipIn && r.chipText <= 0, `the model's chip is whole: ${JSON.stringify(r)}`);
      // a status beside the head keeps room enough to be read
      if (beside) assert(r.emW >= 40, `the status keeps some room: ${JSON.stringify(r)}`);
    }
    if (how !== "failed") {
      const lit = rows.find((r) => r.em.includes(want));
      assert(lit, `the answering row says the intent: ${JSON.stringify(rows)}`);
      assert(lit.chip.startsWith(how === "fold" ? "gpt-6.1-terra-preview" : "gpt-6.1-terra"), JSON.stringify(lit));
      // the status cut short beside the name is read whole over it
      if (beside && how !== "fold") assert(lit.emCut && lit.tip.includes(intent), `the long status is the one cut, read whole on hover: ${JSON.stringify(lit)}`);
    }
    // every status cut short says itself whole when pointed at
    const cut = await page.evaluate(() => [...document.querySelectorAll(".rt-accts li:not(.rt-gap):not(.rt-sub) > em")]
      .map((em, i) => [i, em.scrollWidth > em.clientWidth + 1]).filter(([, c]) => c).map(([i]) => i));
    for (const i of cut) {
      const em = page.locator(".rt-accts li:not(.rt-gap):not(.rt-sub) > em").nth(i);
      await em.hover();
      const [text, tip, rest] = await em.evaluate((e) => [e.textContent, e.title, e.parentElement.classList.contains("rest") && !e.parentElement.classList.contains("on")]);
      assert(rest ? tip : tip === text, `a status cut short is read whole over it: ${JSON.stringify({ text, tip })}`);
    }
    if (beside && how === "live" && lang === "en" && width === 1600) assert(cut.length >= 2, `this fixture cuts the intent's and the 429's status: ${cut}`);
    const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    assert(wide <= 0, `no sideways scroll: ${wide}px`);
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.locator(".rt-accts").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${how}.png`) });
    }
    assert.deepEqual(errors, []);
  });
}
