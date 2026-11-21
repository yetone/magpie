// Run with Node's test runner and Playwright on the module path; see README.md.
// Insights › Masking's search (r3inbowari on X: so many words are masked
// that it's hard to tell whether one was): a field over the requests
// asks the server for the records with a value that has the word in it
// (the page has only the newest 40), keeps its focus and caret while the
// list is read again as it is typed, inks the kind of the value found,
// says so when nothing has the word and when values from before magpie
// last started couldn't be looked at, and Escape clears it. On a narrow
// page an empty field is a magnifier, so the bar keeps one line. The word goes
// to /api/masking/records alone. Every language, Chromium and WebKit,
// 1100 and 420px; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const KEY = "{{API_KEY_aaaa1111}}", MAIL = "{{EMAIL_cccc3333}}", NAME = "{{PERSON_bbbb2222}}";
// what the masker remembers: the PERSON value is from before a restart
const KNOWN = { [KEY]: "sk-proj-abcdEFGH1234ijkl", [MAIL]: "ann@example.com" };
const K1 = "1792987654321000001", K2 = "1792987654321000002", K3 = "1792987654321000003";
const ago = (ms) => new Date(Date.now() - ms).toISOString();
const records = [
  { key: K3, t: ago(60e3), what: "chat", agent: "claude", provider: "anthropic", model: "claude-opus-4-1", n: 2,
    values: [{ p: KEY, k: "API_KEY", b: "rule", h: "sk-p", l: 24, n: 1, known: true }, { p: MAIL, k: "EMAIL", b: "rule", h: "a", l: 15, n: 1, known: true }] },
  { key: K2, t: ago(120e3), what: "chat", agent: "codex", provider: "openai", model: "gpt-5", n: 1,
    values: [{ p: MAIL, k: "EMAIL", b: "rule", h: "a", l: 15, n: 1, known: true }] },
  { key: K1, t: ago(300e3), what: "chat", agent: "codex", provider: "openai", model: "gpt-5", n: 1,
    values: [{ p: NAME, k: "PERSON", b: "model", h: "", l: 3, n: 1 }] },
];
const summary = (days) => ({
  days: Array.from({ length: days }, () => ({ day: "2026-11-20", requests: 1, n: 1, distinct: 1 })),
  requests: 3, n: 4, distinct: 3, blocked: 0, hours: [],
  kinds: [{ name: "EMAIL", n: 2, distinct: 1 }, { name: "API_KEY", n: 1, distinct: 1 }, { name: "PERSON", n: 1, distinct: 1 }],
  by: { rule: 2, model: 1 },
  agents: [{ name: "claude", n: 2, distinct: 2 }, { name: "codex", n: 2, distinct: 2 }],
  providers: [{ name: "anthropic", n: 2, distinct: 2 }, { name: "openai", n: 2, distinct: 2 }],
  checked: [], values: [], since: ago(7 * 86400e3),
});

function serve(lang, sent) {
  const settings = { theme: "light", lang, tray: "panel", currency: "usd", redact: true, redactPersonal: true };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname.startsWith("/api/")) sent.push(url.pathname + url.search + " " + (req.postData() || ""));
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }, { id: "codex", name: "Codex", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "anthropic", name: "Anthropic", agents: [], models: [] }, { id: "openai", name: "OpenAI", agents: [], models: [] }], presets: [], excluded: [], gateway: { running: true, calls: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/masking/summary") return json(summary(+url.searchParams.get("days")));
    if (url.pathname === "/api/masking/records") {
      // as the server does: a value's text as the masker remembers it,
      // whatever its case, or the placeholder as written
      const q = url.searchParams.get("q")?.toLowerCase();
      let unknown = false;
      const takes = (v) => {
        if (v.p.toLowerCase() === q) return true;
        if (!KNOWN[v.p]) { unknown = true; return false; }
        return KNOWN[v.p].toLowerCase().includes(q);
      };
      const rs = records.filter((r) => (!url.searchParams.get("agent") || r.agent === url.searchParams.get("agent")) && (!q || r.values.some(takes)))
        .map((r) => (q ? { ...r, hits: r.values.filter(takes).map((v) => v.p) } : r));
      return json({ records: rs, more: false, ...(q ? { unknown } : {}) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const wide = (page) => page.evaluate(() => {
  const over = [...document.querySelectorAll("#view-masking *")].filter((n) => n.getClientRects().length && n.getBoundingClientRect().right > innerWidth + 1).map((n) => n.className?.baseVal ?? n.className);
  return { page: document.documentElement.scrollWidth > document.documentElement.clientWidth, over };
});
const english = ["Find a masked word", "No masked value has", "remembers masked values only while it runs", "Only the requests these filters show"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the masking page finds a masked word`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], sent = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, sent));
        await page.goto("http://magpie.test/?view=masking");
        const list = page.locator("#maskList"), q = page.locator("#maskQ"), rows = list.locator(".msk-row");
        await rows.nth(2).waitFor();
        await q.waitFor();
        const hint = await q.getAttribute("placeholder");
        assert(hint, "the field says what it finds");
        if (lang !== "en") assert.notEqual(hint, "Find a masked word", "its hint is translated");

        // typed a key at a time, the field keeps its focus and caret as
        // the list is read again, and the server is asked for the word
        // on a phone's page an empty field waits as a magnifier at the
        // bar's end, so the requests start where they did; tapped, it is
        // a line of its own
        if (width < 760) assert((await q.boundingBox()).width <= 32, "an empty search is a magnifier");
        await q.click();
        if (width < 760) assert((await q.boundingBox()).width > 200, "tapped, it takes the line");
        await page.keyboard.type("ANN@", { delay: 120 });
        await page.waitForTimeout(400);
        await page.keyboard.type("example", { delay: 40 });
        const asked = (w) => sent.some((s) => s.startsWith("/api/masking/records?") && new URLSearchParams(s.split(" ")[0].split("?")[1]).get("q") === w);
        for (const end = Date.now() + 8000; !asked("ANN@example");) {
          assert(Date.now() < end, "the server is asked for the word: " + sent.join("\n"));
          await page.waitForTimeout(25);
        }
        await page.waitForTimeout(300);
        await page.waitForFunction(() => document.querySelectorAll("#maskList .msk-row").length === 2);
        assert.equal(await q.inputValue(), "ANN@example");
        assert.equal(await page.evaluate(() => document.activeElement?.id), "maskQ", "the field keeps its focus");
        assert.deepEqual(await page.evaluate(() => [document.activeElement.selectionStart, document.activeElement.selectionEnd]), [11, 11], "and its caret");
        // the kind of the value it was found for comes first, inked
        const firstKind = rows.first().locator(".msk-kind").first();
        assert.match(await firstKind.getAttribute("class"), /\bhit\b/);
        assert.equal(await rows.nth(1).locator(".msk-kind.hit").count(), 1);
        // a record from before the restart was looked at but can't be told
        await list.locator(".msk-qnote").waitFor();
        assert.deepEqual(await wide(page), { page: false, over: [] });

        // nothing has it: said, with the word, and the restart's note
        await q.fill("nothing-like-it");
        await list.locator(".msk-qnone").waitFor();
        assert.equal(await rows.count(), 0);
        assert((await list.locator(".msk-qnone").innerText()).includes("nothing-like-it"));
        await list.locator(".msk-qnote").waitFor();
        assert.deepEqual(await wide(page), { page: false, over: [] });
        if (lang !== "en") {
          const said = await page.locator("#view-masking").innerText();
          for (const w of english) assert(!said.includes(w), `${w} left in English: ${said}`);
        }
        // a placeholder from a vendor's reply finds its request
        await q.fill(KEY);
        await page.waitForFunction(() => document.querySelectorAll("#maskList .msk-row").length === 1);
        assert.equal(await rows.first().getAttribute("data-key"), K3);

        // Escape clears it, and every request is back
        await q.focus();
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelectorAll("#maskList .msk-row").length === 3);
        assert.equal(await q.inputValue(), "");
        assert.equal(await list.locator(".msk-qnote, .msk-qnone").count(), 0);
        // the word went to the records' API and nowhere else
        for (const s of sent) if (/nothing-like-it|ANN@example/i.test(decodeURIComponent(s))) assert.match(s, /^\/api\/masking\/records\?/, s);
        assert.deepEqual(errors, []);
      });
    }
  }
}
