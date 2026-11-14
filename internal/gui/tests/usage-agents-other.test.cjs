// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Agents list folds the clients that are small and idle
// into one Other row at its foot (WJaunita45604 on X: dozens of randomly
// named test clients of ~10K tokens each made the page crawl). A client
// folds when it is past the 5 with the most tokens, under 1% of the
// tokens, and either made no call in the last 3 days or is past the top
// 20. The top clients always show; a small client used today stays in
// sight. The Other row's numbers are the folded ones' sum, so the totals
// are exact. It opens and closes with a click or Enter, the page left
// where it is, and shows its rows 100 at a time. Every language, a wide
// window, a narrow one and magpie web on a phone; no backend, the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const now = Date.now();
const ago = (ms) => new Date(now - ms).toISOString();
const H = 3600e3, D = 24 * H;
const G = (id, name, tokens, last, icon) => ({ id, name, icon: icon || "generic", calls: 10, input: tokens - 100, output: 100, cache_read: 0, cache_write: 0, cost: 0.01, ...(last ? { last } : {}) });
// the clients kept in sight, in token order
const KEPT = [
  G("bun", "Bun", 5e6, ago(H)),
  G("siyuan", "SiYuan", 2e6, ago(H)),
  G("translate-web", "translate-web", 1e6, ago(H)),
  G("busy", "busy", 300000, ago(10 * D)), // idle, but over 1%: kept
  G("mid", "mid", 200000, ago(H)),
  G("today", "today", 15000, ago(H)), // small, used an hour ago, in the top 20
  G("unknown", "unknown", 11000), // no last call said: not called idle
];
// the folded ones: 150 test clients idle for 5 days, and one used today
// past the top 20
const FOLDED = [];
for (let i = 0; i < 150; i++) FOLDED.push(G("t" + (1000 + i) + "q", (1000 + i) + "q", 10000 - i, ago(5 * D)));
FOLDED.push(G("late", "late", 5000, ago(H)));
const AGENTS = [...KEPT, ...FOLDED];
const TOTAL = AGENTS.reduce((n, g) => n + g.input + g.output, 0);
const USAGE = {
  calls: 10 * AGENTS.length, errors: 0, input: TOTAL - 1000, output: 1000, reasoning: 0, cache_read: 0, cache_write: 0, unpriced: 0, cost: 0.01 * AGENTS.length, bucket: "day",
  series: [],
  path: "~/.config/magpie/usage.jsonl",
  agents: AGENTS,
  models: [{ ...G("m", "glm-5", TOTAL), model: "glm-5" }],
};

function server(lang, web) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const settings = () => ({ lang, theme: "light" });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [], profiles: [], settings: settings(), fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(USAGE);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { other: "Other", clients: (n) => `${n} clients`, more: (n) => `Show ${n} more` },
  zh: { other: "其他", clients: (n) => `${n} 个客户端`, more: (n) => `显示其余 ${n} 个` },
  "zh-TW": { other: "其他", clients: (n) => `${n} 個用戶端`, more: (n) => `顯示其餘 ${n} 個` },
  ja: { other: null, clients: (n) => `${n} 個のクライアント`, more: null },
  de: { other: null, clients: (n) => `${n} Clients`, more: null },
};
// bring wheels the view as a reader does, in steps as long as the way to
// the control, till it is in sight: 100 rows are far for 20px steps
async function bring(p, loc) {
  await loc.waitFor({ state: "visible" });
  const view = p.locator("#view-usage");
  const v = await view.boundingBox();
  await p.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  for (let i = 0; i < 60; i++) {
    const b = await loc.boundingBox();
    if (b.y >= v.y && b.y + b.height <= v.y + v.height) { await p.waitForTimeout(150); return; }
    const d = b.y < v.y ? b.y - v.y - 40 : b.y + b.height - (v.y + v.height) + 40;
    const was = await view.evaluate((e) => e.scrollTop);
    await p.mouse.wheel(0, Math.max(-600, Math.min(600, d)));
    await p.waitForFunction(([was]) => document.querySelector("#view-usage").scrollTop !== was, [was], { timeout: 2000 }).catch(() => {});
  }
  throw new Error("not in sight: " + JSON.stringify({ view: v, target: await loc.boundingBox() }));
}
const SIZES = [{ width: 1200, web: false }, { width: 560, web: false }, { width: 390, web: true }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Usage page folds small, idle clients into Other", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const { width, web } of SIZES) {
        const w = L[lang];
        await t.test(`${lang} ${width}${web ? " web" : ""}`, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce", hasTouch: web, isMobile: web && engine === "chromium" });
          const p = await ctx.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang, web));
          await p.goto("http://magpie.test/");
          const nav = p.locator('[data-view="usage"]:visible').first();
          if (await nav.count()) await nav.click();
          else await p.evaluate(() => show("usage"));
          await p.locator("#usageAgents .row.stat").first().waitFor();

          const ids = (sel) => p.locator(sel).evaluateAll((rs) => rs.map((r) => r.dataset.id));
          const top = () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
          const fmt = (n) => p.evaluate((n) => fmtN(n), n);

          // the top clients and the small one used today stay; the rest is
          // one row at the foot
          assert.deepEqual(await ids("#usageAgents > .row.stat"), [...KEPT.map((g) => g.id), ""]);
          const other = p.locator("#usageAgents > .row.stat.us-other");
          assert.equal(await other.count(), 1);
          if (w.other) assert.equal((await other.locator(".who .name").textContent()).trim(), w.other);
          const sub = (await other.locator(".who .sub").textContent()).trim();
          assert(sub.startsWith(w.clients(FOLDED.length)), `the Other row says how many it holds: ${sub}`);
          assert.equal(await other.locator(".us-more").count(), 0, "Other has no actions of its own");
          assert.equal(await other.getAttribute("aria-expanded"), "false");
          assert.equal(await p.locator("#usageAgents .us-folded").count(), 0, "the folded rows aren't drawn while closed");
          assert.equal(await p.locator("#usageAgents .row.stat").count(), KEPT.length + 1);

          // its numbers are the folded ones' sum: every row adds up to the total
          const foldedTokens = FOLDED.reduce((n, g) => n + g.input + g.output, 0);
          assert.equal((await other.locator(".num b").textContent()).trim(), await fmt(foldedTokens));
          const shown = await p.evaluate(() => {
            const u = usage;
            const { kept, folded } = usageFoldAgents(u.agents, usageTokens(u));
            return [kept.reduce((n, g) => n + usageTokens(g), 0) + usageTokens(usageSum(folded)), usageTokens(u), usageSum(folded).calls, usageSum(folded).cost];
          });
          assert.equal(shown[0], shown[1], "kept and folded add up to the page's tokens");
          assert.equal(shown[2], 10 * FOLDED.length, "and to its calls");
          assert(Math.abs(shown[3] - 0.01 * FOLDED.length) < 1e-9, "and to its cost");

          // a click opens it, the page left where it is; 100 rows at a time
          await reader.inView(p, other);
          let at = await top();
          await other.click();
          assert.equal(await other.getAttribute("aria-expanded"), "true");
          const folded = "#usageAgents .us-folded > .row.stat";
          assert.equal(await p.locator(folded).count(), 100);
          assert.deepEqual((await ids(folded)).slice(0, 3), FOLDED.slice(0, 3).map((g) => g.id));
          assert.equal(await top(), at, "opening Other doesn't move the page");
          assert.equal(await p.locator(folded + " .us-more").count(), 100, "a folded row keeps its actions");
          const moreBtn = p.locator("#usageAgents .us-folded-more");
          if (w.more) assert.equal((await moreBtn.textContent()).trim(), w.more(FOLDED.length - 100));
          await bring(p, moreBtn);
          at = await top();
          await moreBtn.click();
          await p.waitForFunction((n) => document.querySelectorAll("#usageAgents .us-folded > .row.stat").length === n, FOLDED.length);
          assert.equal(await moreBtn.count(), 0);
          assert.equal(await top(), at, "showing more doesn't move the page");
          assert.equal((await ids(folded)).at(-1), "late");

          // nothing is wider than the page, and every row's action stays in it
          const wide = await p.evaluate(() => { const v = document.querySelector("#view-usage"); return [v.scrollWidth, v.clientWidth, document.documentElement.scrollWidth, window.innerWidth]; });
          assert(wide[0] <= wide[1] + 1 && wide[2] <= wide[3] + 1, "no sideways scroll: " + wide);
          const out = await p.evaluate(() => [...document.querySelectorAll("#usageAgents .row.stat")].filter((r) => {
            const box = r.getBoundingClientRect();
            return [...r.querySelectorAll(".us-more")].some((b) => { const x = b.getBoundingClientRect(); return x.right > box.right + 0.5 || x.left < box.left - 0.5; });
          }).map((r) => r.dataset.id));
          assert.deepEqual(out, [], "every row's action stays inside its row");

          // Enter closes it, the focus kept on the row
          await bring(p, other);
          const rowAt = () => other.evaluate((e) => e.getBoundingClientRect().top);
          const was = await rowAt();
          await other.focus();
          await p.keyboard.press("Enter");
          assert.equal(await other.getAttribute("aria-expanded"), "false");
          assert.equal(await p.locator("#usageAgents .us-folded").count(), 0);
          assert.equal(await p.evaluate(() => document.activeElement?.classList.contains("us-other")), true);
          // the row stays where it was on the screen, unless the page is
          // now too short to be scrolled that far: then it is at its end
          const end = await p.locator("#view-usage").evaluate((v) => v.scrollTop + v.clientHeight >= v.scrollHeight - 1);
          const now = await rowAt();
          assert(Math.abs(now - was) < 1.5 || (end && now > was), `closing Other leaves its row where it was: ${was} → ${now}`);
          // and Space opens it again, 100 at a time once more
          await p.keyboard.press(" ");
          assert.equal(await p.locator(folded).count(), 100);

          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}

// The rule itself, apart from the page: two or more fold or none do, a
// client with no last call is not idle, and rank comes from tokens.
test("the fold rule", async (t) => {
  const browser = await chromium.launch({ channel: "chromium" });
  t.after(() => browser.close());
  const ctx = await browser.newContext();
  const p = await ctx.newPage();
  await p.route("**/*", server("en", false));
  await p.goto("http://magpie.test/");
  await p.waitForFunction(() => typeof usageFoldAgents === "function");
  const fold = (groups) => p.evaluate(([groups, now]) => {
    const total = groups.reduce((n, g) => n + usageTokens(g), 0);
    return usageFoldAgents(groups, total, now).folded.map((g) => g.id);
  }, [groups, now]);
  const top5 = [G("a", "a", 1e6), G("b", "b", 1e6), G("c", "c", 1e6), G("d", "d", 1e6), G("e", "e", 1e6)];
  // one idle small client alone stays a row
  assert.deepEqual(await fold([...top5, G("x", "x", 100, ago(5 * D))]), []);
  // two do fold
  assert.deepEqual(await fold([...top5, G("x", "x", 100, ago(5 * D)), G("y", "y", 100, ago(4 * D))]), ["x", "y"]);
  // under 3 days is not idle; no last is not idle
  assert.deepEqual(await fold([...top5, G("x", "x", 100, ago(2 * D)), G("y", "y", 100)]), []);
  // the top 5 never fold, small and idle as they are
  assert.deepEqual(await fold([G("a", "a", 100, ago(9 * D)), G("b", "b", 100, ago(9 * D)), G("c", "c", 1e6)]), []);
  // the order given doesn't decide the rank
  assert.deepEqual(await fold([G("x", "x", 100, ago(5 * D)), G("y", "y", 100, ago(4 * D)), ...top5]), ["x", "y"]);
  await ctx.close();
});
