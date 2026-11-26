// Run with Node's test runner and Playwright on the module path; see README.md.
// A session's conversation reads as its turns (usrname1833 on Discord): the
// user's words and the agent's last words in a turn stand out, in the
// interface's font and larger than the rest, and everything between them
// (thinking, tool calls and results, what the agent said on the way) folds
// to one line, "N steps", with a chevron. A fold's steps are drawn only
// when it is first opened, so a long session's page holds what was said
// and one line a turn. A click on the line shows the steps,
// each to its first lines; a click on a long one (or Enter) shows it whole,
// and again folds it back. What was unfolded stays so when the sheet is
// drawn again. A turn with no last words shows its fold only. A search
// that found words inside a fold opens it at them, and the turns are still
// what the head's turn buttons count. Nothing has a left-border accent or a
// gradient, the chevron turns in under 300ms on the strong ease-out and not
// at all with reduced motion, and nothing overflows at 390px. Chromium and
// WebKit, every language, wide and narrow, light and dark.
// MAGPIE_ASSETS selects older assets (red there); ARTIFACT_DIR keeps
// screenshots of the sheet as it opens and unfolded.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const CALL = JSON.stringify({ command: "grep -rn foo .", cwd: "/work/pi", timeout: 120, env: { LANG: "C" }, why: "find every use of foo before renaming it" }, null, 2);
const PARTS = [
  { role: "user", kind: "text", text: "Rename foo to bar" },
  { role: "assistant", kind: "thinking", text: "grep for it first" },
  { role: "assistant", kind: "tool_use", name: "bash", text: CALL },
  { role: "tool", kind: "tool_result", name: "bash", text: "a.go:3: foo()" },
  { role: "assistant", kind: "text", text: "Found it in a.go, next to the Zanzibar key." },
  { role: "assistant", kind: "tool_use", name: "edit", text: '{"path":"a.go"}' },
  { role: "tool", kind: "tool_result", name: "edit", text: "ok" },
  { role: "assistant", kind: "text", text: "Renamed foo to bar in a.go." },
  { role: "user", kind: "text", text: "Now run the tests" },
  { role: "assistant", kind: "tool_use", name: "bash", text: '{"command":"go test ./..."}' },
  { role: "tool", kind: "tool_result", name: "bash", text: "ok  example.com/a  0.1s" },
  { role: "user", kind: "text", text: "Thanks" },
  { role: "assistant", kind: "thinking", text: "they are done" },
  { role: "assistant", kind: "text", text: "You're welcome." },
];
const KEYS = ["1 step", "{n} steps", "Whole conversation", "Messages only", "Only what the user and the agent said, without thinking or tool calls"];
const NAV = { en: "Sessions", zh: "会话", "zh-TW": "會話", ja: "セッション", de: "Sitzungen" };

function serve(lang, theme, reads) {
  const s = { agent: "pi", id: "p-1", cwd: "/work/pi", title: "Rename foo", start: at(10), last: at(5), models: [], size: 4096, messages: 14, deletable: true, transcript: true };
  const state = { asked: true, all: true, found: [], gateway: false };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/reads") return json(state);
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "pi", count: 1, deletable: true, name: "Pi", icon: "pi" }], agent: "pi", sessions: [s], terminal: false, trash: [], trashDir: "~/trash", reads: state });
    }
    if (url.pathname === "/api/sessions/search") {
      const hits = url.searchParams.get("q") === "zanzibar" ? [{ id: "p-1", role: "assistant", kind: "text", before: "next to the ", match: "Zanzibar", after: " key." }] : [];
      return json({ hits, searched: 1, total: 1 });
    }
    if (url.pathname === "/api/sessions/said") return json({ hits: [], indexed: 1, total: 1 });
    if (url.pathname === "/api/sessions/transcript") {
      reads.push(url.searchParams.get("find") || "");
      return json({ parts: PARTS });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const runs = [];
for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 390]) runs.push([lang, width, "light"]);
runs.push(["en", 1100, "dark"], ["zh", 390, "dark"]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width, theme] of runs) {
    test(`${engine} ${lang} ${width} ${theme}: a turn shows what was said, its steps folded to one line`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const reads = [];
      await page.route("**/*", serve(lang, theme, reads));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: NAV[lang], exact: true }).click();
      const view = page.locator("#view-sessions");
      const row = view.locator('.row.sm-sess[data-id="p-1"]');
      await row.waitFor();
      await row.locator(".who").click();
      await view.locator(".sess-detail .sess-talk-btn").click();
      const talk = page.locator(".sess-sheet .sess-talk");
      await talk.locator(".cx-part").first().waitFor();
      const shot = async (name) => {
        if (!process.env.ARTIFACT_DIR) return;
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(".sess-sheet").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${theme}-${name}.png`) });
      };
      await shot("opened");
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      const y0 = await page.evaluate(() => scrollY);

      // each turn: the user's words, its steps on one line, its last words
      assert.equal(await talk.locator(".cx-part").count(), 5, "what was said is drawn: three asks, two answers");
      assert.equal(await talk.locator("[data-turn]").count(), 3, "the turns the head counts");
      const order = await talk.evaluate((b) => [...b.children].filter((n) => !n.matches(".cx-none, .cx-src")).map((n) =>
        n.matches(".sess-steps") ? "steps" : n.matches(".sess-ask") ? "ask:" + n.querySelector(".cx-t").textContent : n.matches(".sess-final") ? "final:" + n.querySelector(".cx-t").textContent : "other"));
      assert.deepEqual(order, ["ask:Rename foo to bar", "steps", "final:Renamed foo to bar in a.go.", "ask:Now run the tests", "steps", "ask:Thanks", "steps", "final:You're welcome."]);
      const folds = talk.locator("details.sess-steps");
      assert.deepEqual((await folds.locator(":scope > summary").allTextContents()).map((x) => x.trim()),
        [await tr("{n} steps", { n: 6 }), await tr("{n} steps", { n: 2 }), await tr("1 step")]);
      assert.equal(await folds.locator(":scope > summary svg").count(), 3, "each fold has its chevron");
      assert.deepEqual(await folds.evaluateAll((ds) => ds.map((d) => d.open)), [false, false, false], "folded at first");
      assert.deepEqual(await folds.evaluateAll((ds) => ds.map((d) => d.children.length)), [1, 1, 1], "a shut fold's steps aren't drawn yet");
      // a closed fold is its one line: WebKit's isVisible counts a closed <details>' children as shown, so measure it
      for (const h of await folds.evaluateAll((ds) => ds.map((d) => d.getBoundingClientRect().height))) assert(h < 40, `a closed fold is ${h}px tall`);
      for (const n of await talk.locator(".sess-ask, .sess-final").all()) assert(await n.isVisible());

      // the user's words and the answer stand out from the steps
      await folds.first().locator(":scope > summary").click();
      const look = await talk.evaluate((b) => {
        const f = (sel) => { const cs = getComputedStyle(b.querySelector(sel)); return { size: parseFloat(cs.fontSize), family: cs.fontFamily, color: cs.color }; };
        return { ask: f(".sess-ask .cx-t"), final: f(".sess-final .cx-t"), step: f(".sess-steps > .cx-part:not(details) .cx-t") };
      });
      for (const k of ["ask", "final"]) {
        assert(look[k].size >= look.step.size + 1.5, `${k} is larger than a step: ${JSON.stringify(look)}`);
        assert.notEqual(look[k].family, look.step.family, `${k} is in the interface's font, a step in the code font`);
      }
      // quiet: no left-border accent, no gradient, anywhere in it
      const loud = await talk.evaluate((b) => [b, ...b.querySelectorAll("*")].flatMap((n) => {
        const cs = getComputedStyle(n), out = [];
        if (cs.borderLeftWidth !== cs.borderRightWidth || cs.borderLeftColor !== cs.borderRightColor) out.push("left border: " + n.className);
        if (/gradient/.test(cs.backgroundImage)) out.push("gradient: " + n.className);
        return out;
      }));
      assert.deepEqual(loud, []);
      // the chevron's turn: short, the strong ease-out
      const turn = await folds.first().locator(":scope > summary svg").evaluate((s) => ({ d: getComputedStyle(s).transitionDuration, f: getComputedStyle(s).transitionTimingFunction }));
      assert(parseFloat(turn.d) > 0 && parseFloat(turn.d) <= 0.3, JSON.stringify(turn));
      assert.equal(turn.f, "cubic-bezier(0.23, 1, 0.32, 1)");

      // unfolded: each step on its line or first lines
      const steps = folds.first().locator(":scope > .cx-part");
      assert.equal(await steps.count(), 6);
      for (const n of await steps.all()) assert(await n.isVisible());
      assert.equal(await folds.first().locator(":scope > details.cx-part").getAttribute("open"), null, "thinking stays on its line");
      const call = folds.first().locator(":scope > .cx-part.brief");
      assert.equal(await call.count(), 1, "only the long call is cut to its first lines");
      const size = () => call.locator(".cx-t").evaluate((p) => ({ h: p.clientHeight, all: p.scrollHeight, line: parseFloat(getComputedStyle(p).lineHeight) }));
      const cut = await size();
      assert(cut.all > cut.h + 4 && cut.h <= cut.line * 3 + 20, `its first lines: ${JSON.stringify(cut)}`);
      assert.equal(await call.locator(".cx-more").count(), 0, "no second button for it");
      await shot("steps");
      await call.click();
      assert.equal(await call.getAttribute("aria-expanded"), "true");
      const whole = await size();
      assert(whole.all <= whole.h + 1, `shown whole: ${JSON.stringify(whole)}`);
      await call.click();
      assert.equal(await call.getAttribute("aria-expanded"), "false");
      await call.focus();
      await page.keyboard.press("Enter");
      assert.equal(await call.getAttribute("aria-expanded"), "true", "Enter shows it whole too");

      // every fold opened, every part is there once
      for (const s of await folds.locator(":scope > summary").all()) if (!(await s.evaluate((x) => x.parentNode.open))) await s.click();
      assert.equal(await talk.locator(".cx-part").count(), PARTS.length, "every part once its fold is open");
      for (const s of (await folds.locator(":scope > summary").all()).slice(1)) await s.click();
      assert.deepEqual(await folds.evaluateAll((ds) => ds.map((d) => d.open)), [true, false, false], "the clicks shut the last two");
      // drawn again, what was unfolded stays so
      await talk.evaluate((b) => b.again());
      assert.deepEqual(await folds.evaluateAll((ds) => ds.map((d) => d.open)), [true, false, false]);
      assert.equal(await folds.first().locator(":scope > .cx-part.brief").getAttribute("aria-expanded"), "true");
      assert.equal(await page.evaluate(() => scrollY), y0, "no click moved the page");
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll");
      const fit = await talk.evaluate((b) => [...b.querySelectorAll(".sess-steps > summary, .cx-part")].every((n) => n.getBoundingClientRect().right <= b.getBoundingClientRect().right + 1));
      assert(fit, `everything fits at ${width}`);
      await shot("unfolded");
      await folds.first().locator(":scope > summary").click();
      assert.equal(await folds.first().evaluate((d) => d.open), false);

      // with reduced motion the chevron doesn't turn
      await page.emulateMedia({ reducedMotion: "reduce" });
      const still = parseFloat(await folds.first().locator(":scope > summary svg").evaluate((s) => getComputedStyle(s).transitionDuration));
      assert(still < 0.001, `the chevron turns in ${still}s with reduced motion`);
      await page.keyboard.press("Escape");
      await page.locator(".sess-sheet").waitFor({ state: "detached" });

      // words a search found inside a fold: it opens there, at them
      await view.locator(".sm-filter").pressSequentially("zanzibar", { delay: 10 });
      await row.locator(".sm-hit").click();
      const found = page.locator(".sess-sheet .sess-found");
      await found.waitFor();
      assert.equal(reads.at(-1), "zanzibar");
      assert.match(await found.textContent(), /Zanzibar key/);
      assert(await found.isVisible(), "the fold that holds it is open");
      assert.deepEqual(await page.locator(".sess-sheet details.sess-steps").evaluateAll((ds) => ds.map((d) => d.open)), [true, false, false]);
      assert.deepEqual(await page.locator(".sess-sheet details.sess-steps").evaluateAll((ds) => ds.map((d) => d.children.length)), [7, 1, 1], "only the fold that holds it is drawn");
      await page.keyboard.press("Escape");

      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang, KEYS]);
        assert.deepEqual(missing, [], `${lang} has every string`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
