// Run with Node's test runner and Playwright on the module path; see README.md.
// A long session's conversation is read from the agent's file a part at a
// time, and the sheet never stops short of its last turns unsaid
// (usrname1833 on Discord: a long session's sheet didn't show all its
// turns). Where the first read was cut, the sheet ends in "Show the rest";
// a click reads the parts after those shown (?after=n) and adds them, the
// turns and folds drawn again, until the session's end, where the button
// is gone. Nothing the user didn't click moves the page. A session the
// gateway recorded has no rest to read: it keeps its line, with no button.
// Chromium and WebKit, English and Chinese, wide and narrow.
// MAGPIE_ASSETS selects older assets (red there).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const PARTS = [
  { role: "user", kind: "text", text: "Rename foo to bar" },
  { role: "assistant", kind: "thinking", text: "grep for it first" },
  { role: "assistant", kind: "tool_use", name: "bash", text: '{"command":"grep -rn foo ."}' },
  { role: "tool", kind: "tool_result", name: "bash", text: "a.go:3: foo()" },
  { role: "assistant", kind: "text", text: "Renamed foo to bar in a.go." },
  { role: "user", kind: "text", text: "Now run the tests" },
  { role: "assistant", kind: "tool_use", name: "bash", text: '{"command":"go test ./..."}' },
  { role: "tool", kind: "tool_result", name: "bash", text: "ok  example.com/a  0.1s" },
  { role: "assistant", kind: "text", text: "They pass." },
  { role: "user", kind: "text", text: "Thanks" },
  { role: "assistant", kind: "thinking", text: "they are done" },
  { role: "assistant", kind: "text", text: "You're welcome." },
];
// the agent's file read in three: cut after 6 parts, then after 9
const READS = { 0: { parts: PARTS.slice(0, 6), cut: true }, 6: { parts: PARTS.slice(6, 9), cut: true, from: 6 }, 9: { parts: PARTS.slice(9), from: 9 } };
const WORDS = { en: { nav: "Sessions", rest: "Show the rest", more: "There was more than is shown here" }, zh: { nav: "会话", rest: "显示其余部分", more: "内容太多，这里只显示了一部分" } };

function serve(lang, asked) {
  const s = (id, title) => ({ agent: "pi", id, cwd: "/work/pi", title, start: at(10), last: at(5), models: [], size: 4096, messages: 12, deletable: true, transcript: true });
  const state = { asked: true, all: true, found: [], gateway: false };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/reads") return json(state);
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "pi", count: 2, deletable: true, name: "Pi", icon: "pi" }], agent: "pi", sessions: [s("p-1", "Long one"), s("g-1", "Recorded one")], terminal: false, trash: [], trashDir: "~/trash", reads: state });
    }
    if (url.pathname === "/api/sessions/search" || url.pathname === "/api/sessions/said") return json({ hits: [], searched: 1, indexed: 1, total: 1 });
    if (url.pathname === "/api/sessions/transcript") {
      const id = url.searchParams.get("id"), after = url.searchParams.get("after");
      asked.push(id + (after === null ? "" : "@" + after));
      if (id === "g-1") return json({ parts: PARTS.slice(0, 6), cut: true, source: "gateway" });
      return json(READS[after || 0] || { parts: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a long session's sheet reads the rest when asked, to its last turn`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const asked = [];
        const w = WORDS[lang];
        await page.route("**/*", serve(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#nav").getByRole("button", { name: w.nav, exact: true }).click();
        const view = page.locator("#view-sessions");
        const open = async (id) => {
          const row = view.locator(`.row.sm-sess[data-id="${id}"]`);
          await row.waitFor();
          await row.locator(".who").click();
          await view.locator(".sess-detail .sess-talk-btn").click();
          await page.locator(".sess-sheet .sess-talk .sess-ask").first().waitFor();
          return page.locator(".sess-sheet .sess-talk");
        };

        const talk = await open("p-1");
        const rest = talk.locator(":scope > .sess-rest");
        assert.equal(await rest.count(), 1, "a cut read ends in a button for the rest");
        assert.equal((await rest.textContent()).trim(), w.rest);
        assert.equal(await rest.evaluate((b) => b.tagName), "BUTTON");
        assert.equal(await talk.locator("[data-turn]").count(), 2);
        assert.equal(await talk.getByText("You're welcome.").count(), 0, "not read yet");
        const y0 = await page.evaluate(() => scrollY);

        await rest.click();
        await page.waitForFunction(() => document.querySelectorAll(".sess-sheet .sess-talk .sess-final").length === 2);
        assert.deepEqual(asked, ["p-1", "p-1@6"]);
        assert.equal(await rest.count(), 1, "still more to read");
        assert(await rest.evaluate((b) => b === document.activeElement), "the button keeps the focus");
        assert.equal(await talk.locator(".sess-final").last().textContent().then((x) => x.includes("They pass.")), true, "the turn read on the way is whole");

        await page.keyboard.press("Enter");
        await talk.getByText("You're welcome.").waitFor();
        assert.deepEqual(asked, ["p-1", "p-1@6", "p-1@9"]);
        assert.equal(await rest.count(), 0, "the end: no button");
        for (const s of await talk.locator("details.sess-steps > summary").all()) await s.click();
        assert.equal(await talk.locator(".cx-part").count(), PARTS.length, "every part once");
        assert.equal(await talk.locator("[data-turn]").count(), 3, "every turn");
        const finals = await talk.locator(".sess-final .cx-t").allTextContents();
        assert.deepEqual(finals.map((x) => x.trim()), ["Renamed foo to bar in a.go.", "They pass.", "You're welcome."]);
        assert.equal(await talk.getByText(w.more).count(), 0);
        assert.equal(await page.evaluate(() => scrollY), y0, "no click moved the page");
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll");
        await page.keyboard.press("Escape");
        await page.locator(".sess-sheet").waitFor({ state: "detached" });

        // what the gateway recorded has no rest to read
        const kept = await open("g-1");
        await kept.getByText(w.more).waitFor();
        assert.equal(await kept.locator(".sess-rest").count(), 0);
        assert.deepEqual(asked, ["p-1", "p-1@6", "p-1@9", "g-1"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
