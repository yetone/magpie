// Run with Node's test runner and Playwright on the module path; see README.md.
// Whether a Codex account spends its credits once its windows run out, a
// standing say of the account's: every named Codex account's card in full
// has a row saying what it does, with a switch, on until turned off, that
// posts settings/codex-credits for that account; an account holding no
// credits has it too, so it can be set ahead. The credits held stay told
// beside it. A GLM team's and a plugin's accounts get none. Off, the row
// says the account is held when a window runs out, and the Routing page
// says why such an account was left out. On the Usage page and the menu
// bar panel, at narrow widths too, nothing of the row is cut off or wider
// than its card, and a click moves nothing. English, Chinese, Japanese and
// German, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = new Date(Date.now() + 3 * 864e5).toISOString();
const now = new Date();
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "Me@example.com", plan: "Plus", lastServedAt: now.toISOString(),
    windows: [{ name: "5 hours", used: 100, resetsAt: later }, { name: "7 days", used: 40, resetsAt: later }],
    balance: "1.2K credits" },
  // a second account, holding no credits: its say is set ahead of any
  { provider: "codex", name: "Codex", icon: "codex-color", user: "two@example.com", plan: "Plus",
    windows: [{ name: "5 hours", used: 10, resetsAt: later }, { name: "7 days", used: 20, resetsAt: later }] },
  { provider: "zhipu", name: "GLM Coding", icon: "zhipu-color", user: "team@example.com", plan: "Team",
    windows: [{ name: "5 hours", used: 30 }] },
  { provider: "codex-plugin", name: "Codex (plugin)", user: "Me@example.com", plan: "Plus",
    windows: [{ name: "7 days", used: 100, resetsAt: later }] },
];
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = new Date(now.getTime() - 60e3).toISOString();
const spare = { id: "codex@spare@example.com", provider: "codex", name: "Codex", kind: "account", who: "spare@example.com", model: "gpt-5.5" };
const held = { id: "codex", provider: "codex", name: "Codex", kind: "account", who: "Me@example.com", model: "gpt-5.5",
  unlisted: true, capped: 100, used: 100, capBack: later, noCredits: true };
const routes = [{ id: 7, seq: 7, time: at, agent: "codex", model: "gpt-5.5", provider: "codex", order: [spare], left: [held],
  tries: [{ id: spare.id, model: "gpt-5.5", start: at, done: true, status: 200, ms: 400 }], done: true, status: 200, ms: 400 }];

function serve(lang, posts) {
  let off = [];
  const settings = () => ({ theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["codex", "zhipu", "codex-plugin"], trayUsageEvery: 3, codexNoCredits: off });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/codex-credits") {
      const body = req.postDataJSON();
      posts.push(body);
      const who = body.user.toLowerCase();
      off = off.filter((u) => u !== who);
      if (!body.on) off.push(who);
      return json(settings());
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    on: "Use credits: when its windows run out, so a task goes on", off: "Use credits: no — held when a window runs out", label: "Use credits",
    turnedOff: (who) => `${who} no longer spends its credits: held when a window runs out`, turnedOn: (who) => `${who} spends its credits when its windows run out`,
    left: "is left out: a usage window is used up, and the account is set not to spend its credits, so it counts as used up until that window renews.",
  },
  zh: {
    on: "使用积分： 额度用尽时继续用积分，任务不中断", off: "使用积分： 否，额度用尽即暂停使用", label: "使用积分",
    turnedOff: (who) => `${who} 不再使用积分：额度用尽即暂停使用`, turnedOn: (who) => `${who} 额度用尽时将使用积分`,
    left: "不参与：某个用量窗口已用完，且该账号设为不使用积分，窗口重置前视为已用完。",
  },
  ja: {
    on: "クレジットを使用： 枠を使い切ったら使用し、タスクを続行", off: "クレジットを使用： しない — 枠を使い切ったら停止", label: "クレジットを使用",
    turnedOff: (who) => `${who} はクレジットを使用しなくなりました：枠を使い切ったら停止します`, turnedOn: (who) => `${who} は枠を使い切るとクレジットを使用します`,
    left: "は除外：使用量ウィンドウの 1 つを使い切り、アカウントはクレジットを使用しない設定のため、そのウィンドウが更新されるまで使い切ったものとみなします。",
  },
  de: {
    on: "Credits nutzen: wenn die Kontingente aufgebraucht sind, damit eine Aufgabe weiterläuft", off: "Credits nutzen: nein – angehalten, wenn ein Kontingent aufgebraucht ist", label: "Credits nutzen",
    turnedOff: (who) => `${who} verbraucht keine Credits mehr: angehalten, wenn ein Kontingent aufgebraucht ist`, turnedOn: (who) => `${who} verbraucht Credits, wenn die Kontingente aufgebraucht sind`,
    left: "wird übergangen: Ein Zeitfenster ist aufgebraucht, und das Konto soll keine Credits verbrauchen, daher gilt es bis zur Erneuerung des Zeitfensters als aufgebraucht.",
  },
};

const keys = [
  "Use credits", "Use credits:", "when its windows run out, so a task goes on", "no — held when a window runs out",
  "On: once one of this account's windows is used up, ChatGPT answers on the account's credits, if it holds any, so a task goes on. Click to turn it off.",
  "Off: once one of this account's windows is used up, magpie holds it till the window renews, and requests go to your other accounts, groups and fallbacks, so its credits aren't spent. With none of them left, a request is refused with why, unless Auto-use resets is on and its week is used up: then a reset is used first.",
  "{who} no longer spends its credits: held when a window runs out", "{who} spends its credits when its windows run out",
  "held: its allowance used up, set not to spend credits",
  "{who} is left out: a usage window is used up, and the account is set not to spend its credits, so it counts as used up until that window renews.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a Codex account spends its credits or is held, a standing say on its card`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-codex-credits-${i}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (url, viewport, posts) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto(url);
        return page;
      };
      const wait = async (posts, n) => { for (let i = 0; i < 60 && posts.length < n; i++) await new Promise((r) => setTimeout(r, 50)); };
      const text = (loc) => loc.evaluate((e) => e.textContent.replace(/\s+/g, " ").trim());
      // a switch's click: it posts, turns, says so, and moves nothing
      const flip = async (page, row, posts, who, want) => {
        const sel = row + " .use-credits";
        const b = page.locator(sel);
        const spot = () => b.evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);
        const was = await spot();
        const n = posts.length;
        await b.click();
        await wait(posts, n + 1);
        assert.deepEqual(posts.at(-1), { user: who, on: want });
        await page.locator(sel + `[aria-checked="${want}"]`).waitFor();
        assert.equal(await b.evaluate((e) => e.classList.contains("on")), want);
        assert.equal(await page.locator("#status").textContent(), want ? w.turnedOn(who) : w.turnedOff(who));
        assert.equal(await text(page.locator(row + " .ar-say")), want ? w.on : w.off);
        await page.waitForTimeout(200);
        assert.deepEqual(await spot(), was, "the click moved the page");
      };
      // the row fits its card: the switch whole and inside it, the words
      // wrapping rather than cut off
      const fits = (page, row, card) => page.locator(row).evaluate((r, card) => {
        const c = r.closest(card).getBoundingClientRect(), b = r.getBoundingClientRect(), s = r.querySelector(".use-credits").getBoundingClientRect();
        const say = r.querySelector(".ar-say");
        return b.right <= c.right + 0.5 && s.right <= c.right + 0.5 && s.width >= 27 && s.height >= 15 && say.scrollWidth <= say.clientWidth + 1;
      }, card);

      for (const width of [900, 440]) {
        const posts = [];
        const page = await open("http://magpie.test/?view=usage", { width, height: 760 }, posts);
        const me = '.quota-credits[data-user="Me@example.com"]';
        await page.locator(me).waitFor();
        assert.equal(await page.locator(".quota-credits").count(), 1, "only Codex's own named accounts in full have the say; the GLM team's and the plugin's don't");
        // the credits held are told on its card, beside the say
        assert.match(await text(page.locator('.subscription-card .quota-balance').first()), /1\.2K credits/);
        assert.equal(await text(page.locator(me + " .ar-say")), w.on);
        const sw = page.locator(me + " .use-credits");
        assert.equal(await sw.getAttribute("role"), "switch");
        assert.equal(await sw.getAttribute("aria-checked"), "true", "on until turned off");
        assert.equal(await sw.getAttribute("aria-label"), w.label);
        assert(await sw.getAttribute("title"));
        assert(await fits(page, me, ".subscription-card"), `the say fits the card at ${width}px`);
        await flip(page, me, posts, "Me@example.com", false);
        assert(await fits(page, me, ".subscription-card"), `the say, off, fits the card at ${width}px`);
        // the account holding none has it too, once in full
        await page.locator('.subscription-account.brief[data-card="codex|two@example.com"] .quota-acct-fold').click();
        const two = '.quota-credits[data-user="two@example.com"]';
        await page.locator(two).waitFor();
        assert.equal(await page.locator(two + " .use-credits").getAttribute("aria-checked"), "true");
        await flip(page, two, posts, "two@example.com", false);
        await flip(page, two, posts, "two@example.com", true);
        const border = await page.evaluate(() => [...document.querySelectorAll(".quota-credits, .quota-credits *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
        assert.deepEqual(border, [], "no left-border accent");
      }

      // the menu bar panel's card, at its own width and narrower
      for (const width of [440, 360]) {
        const posts = [];
        const panel = await open("http://magpie.test/?mode=panel", { width, height: 640 }, posts);
        const psel = '.pq-credits[data-user="Me@example.com"]';
        await panel.locator('#ptabs [data-ptab="usage"]').click();
        await panel.locator(psel).waitFor();
        assert.equal(await panel.locator(".pq-credits").count(), 1, "only the Codex account in sight has it");
        assert.equal(await text(panel.locator(psel + " .ar-say")), w.on);
        assert(await fits(panel, psel, ".pq-card"), `the say fits the panel's card at ${width}px`);
        await flip(panel, psel, posts, "Me@example.com", false);
        assert(await fits(panel, psel, ".pq-card"), `the say, off, fits the panel's card at ${width}px`);
      }

      // the Routing page says why an account held for its credits was left out
      const routing = await open("http://magpie.test/?view=routing", { width: 1100, height: 760 }, []);
      await routing.locator(".rt-days .rt-day").nth(1).click();
      await routing.locator(".rt-req").first().click();
      await routing.waitForTimeout(300);
      const steps = await routing.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => l.textContent));
      assert(steps.some((s) => s.includes(w.left)), JSON.stringify(steps));

      if (lang !== "en") {
        const missing = await routing.evaluate(([keys, lang]) => keys.filter((k) => !I18N[lang][k]), [keys, lang]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
