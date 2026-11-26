// Run with Node's test runner and Playwright on the module path; see README.md.
// The sheet's "turn i / N" counts the whole session, not the part of it
// read so far (usrname1833 on Discord: a long session is read about 18
// turns at a time, so N was only those). The first read of a long session
// says its turns (turns), and a read that starts past the start says the
// turns before it (turnsBefore), so i counts from the session's first
// turn too. A whole session's turns are the ones drawn. Reading the rest
// keeps N. Chromium and WebKit, wide and narrow.
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
  { role: "assistant", kind: "tool_use", name: "bash", text: '{"command":"grep -rn foo ."}' },
  { role: "tool", kind: "tool_result", name: "bash", text: "a.go:3: foo()" },
  { role: "assistant", kind: "text", text: "Renamed foo to bar in a.go." },
  { role: "user", kind: "text", text: "Now run the tests" },
  { role: "assistant", kind: "text", text: "They pass." },
  { role: "user", kind: "text", text: "Thanks" },
  { role: "assistant", kind: "text", text: "You're welcome." },
];
const READS = {
  // the first 4 parts of a 40-turn session, then the next 2
  "p-1": { 0: { parts: PARTS.slice(0, 4), cut: true, turns: 40 }, 4: { parts: PARTS.slice(4, 6), cut: true, from: 4 } },
  // a read from part 6, as a search's: two turns came before it
  "p-2": { 0: { parts: PARTS.slice(6), from: 6, turns: 3, turnsBefore: 2 } },
  // read whole: its turns are the ones drawn
  "w-1": { 0: { parts: PARTS } },
};

function serve(asked) {
  const s = (id, title) => ({ agent: "pi", id, cwd: "/work/pi", title, start: at(10), last: at(5), models: [], size: 4096, messages: 8, deletable: true, transcript: true });
  const state = { asked: true, all: true, found: [], gateway: false };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/reads") return json(state);
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "pi", count: 3, deletable: true, name: "Pi", icon: "pi" }], agent: "pi", sessions: [s("p-1", "Long one"), s("p-2", "Found one"), s("w-1", "Short one")], terminal: false, trash: [], trashDir: "~/trash", reads: state });
    }
    if (url.pathname === "/api/sessions/search" || url.pathname === "/api/sessions/said") return json({ hits: [], searched: 1, indexed: 1, total: 1 });
    if (url.pathname === "/api/sessions/transcript") {
      const id = url.searchParams.get("id"), after = url.searchParams.get("after");
      asked.push(id + (after === null ? "" : "@" + after));
      return json(READS[id]?.[after || 0] || { parts: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 390]) {
    test(`${engine} ${width}: the sheet's turn count is the whole session's`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const asked = [];
      await page.route("**/*", serve(asked));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: "Sessions", exact: true }).click();
      const view = page.locator("#view-sessions");
      const open = async (id) => {
        const row = view.locator(`.row.sm-sess[data-id="${id}"]`);
        await row.waitFor();
        await row.locator(".who").click();
        await view.locator(".sess-detail .sess-talk-btn").click();
        await page.locator(".sess-sheet .sess-talk .sess-ask").first().waitFor();
        return page.locator(".sess-sheet .sess-talk");
      };
      const close = async () => {
        await page.keyboard.press("Escape");
        await page.locator(".sess-sheet").waitFor({ state: "detached" });
      };
      // the sheets here are short, so the last turn is the one read on
      const says = (text) => page.waitForFunction((want) => document.querySelector(".sess-sheet .sess-turns")?.textContent.trim() === want, text)
        .catch(async () => assert.fail(`header says ${JSON.stringify(await page.locator(".sess-sheet .sess-turns").textContent())}, not ${JSON.stringify(text)}`));

      // a long session: N is its 40 turns, though one turn is read
      const talk = await open("p-1");
      assert.equal(await talk.locator("[data-turn]").count(), 1);
      await says("1 / 40");
      // the rest read: still 40, and two turns in
      await talk.locator(":scope > .sess-rest").click();
      await page.waitForFunction(() => document.querySelectorAll(".sess-sheet .sess-talk [data-turn]").length === 2);
      await says("2 / 40");
      await close();

      // read from part 6: the two turns before it are counted
      await open("p-2");
      await says("3 / 3");
      await close();

      // read whole: the turns drawn
      await open("w-1");
      await says("3 / 3");
      await close();

      assert.deepEqual(asked, ["p-1", "p-1@4", "p-2", "w-1"]);
      assert.deepEqual(errors, []);
    });
  }
}
