// Run with Node's test runner and Playwright on the module path; see README.md.
// An open memory's editor holds still while the Memory tab's polls run
// (Nietzsche_Li on X: "资源库-记忆中，被展开的记忆的表单会定时抖动"). Search by
// meaning still reading the memories, or a run learning from conversations,
// is read again every 2s; each read drew the whole page again, and the
// editor faded and slid in anew every time. Now a read that changes nothing
// draws nothing, one that does redraws its own row only, and a render of an
// editor that was open already doesn't replay its entrance. The editor's
// element, its text and caret, and the focus stay. Every language, wide and
// narrow, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const HOME = "/Users/mem";
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", instructions: `${HOME}/.codex/AGENTS.md` }],
  instructions: { shared: "", sets: [{ id: "default", name: "", active: true, text: "" }], agents: [] },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};
const at = (d) => `2026-11-0${d}T10:00:00Z`;
const memories = () => ({
  memories: [
    { id: "a1", text: "Use pnpm, never npm, in this repo", why: "", kind: "rule", scope: "global", origin: "user", status: "active", created: at(1), updated: at(2) },
    { id: "a2", text: "Never push to main", kind: "rule", scope: "global", origin: "user", status: "active", created: at(1), updated: at(1) },
  ],
  projects: {}, digest: true, agents: [],
});

function server(lang, st) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: library });
    if (url.pathname === "/api/memory/semantic") { st.semReads++; return route.fulfill({ json: st.sem }); }
    if (url.pathname === "/api/memory/distill") { st.distReads++; return route.fulfill({ json: st.dist }); }
    if (url.pathname.startsWith("/api/memory")) return route.fulfill({ json: st.mem });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-library";
const langs = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an open memory holds still while the Memory tab polls", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of langs) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 860 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        // search by meaning on, one memory of two read, nothing reading it
        // now (magpie reads new ones once a minute), and a run learning
        // from conversations under way: both polled every 2s
        const st = {
          mem: memories(),
          sem: { on: true, installed: true, size: 1, done: 1, downloading: false, read: 1, of: 2, reading: false },
          dist: { on: true, model: "", auto: "", running: true, models: [] },
          semReads: 0, distReads: 0,
        };
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = page.locator(`${view} .lib-mems .lib-mem[data-id="a1"]`);
        await row.waitFor();
        await row.evaluate((e) => e.click()); // at 420px the header can sit over it while the page settles
        const ta = page.locator(`${view} .lib-memdetail textarea`);
        await ta.waitFor();
        // typed into, the caret in the middle
        await ta.focus();
        await ta.evaluate((e) => e.setSelectionRange(e.value.length, e.value.length));
        await page.keyboard.type(" here");
        await ta.evaluate((e) => e.setSelectionRange(4, 4));
        await page.evaluate(async () => {
          const det = document.querySelector("#view-library .lib-memdetail");
          // its own entrance played out first
          await Promise.all(det.getAnimations().map((a) => a.finished));
          det.__first = true;
          window.__entrances = 0;
          window.__shifts = [];
          document.addEventListener("animationstart", (e) => { if (e.target.closest?.(".lib-detail")) window.__entrances++; }, true);
          // where the editor sits, each frame
          let top0 = det.getBoundingClientRect().top;
          // what is new above it moves it, once: measured from there
          window.__rebase = () => { top0 = document.querySelector("#view-library .lib-memdetail").getBoundingClientRect().top; window.__shifts = []; };
          const look = () => {
            const d = document.querySelector("#view-library .lib-memdetail");
            if (!d) return;
            const r = d.getBoundingClientRect(), o = parseFloat(getComputedStyle(d).opacity);
            if (Math.abs(r.top - top0) > 0.5 || o < 1) window.__shifts.push([r.top - top0, o]);
            requestAnimationFrame(look);
          };
          requestAnimationFrame(look);
        });
        const steady = async (what, moved) => {
          const s = await page.evaluate(() => {
            const det = document.querySelector("#view-library .lib-memdetail");
            const ta = det?.querySelector("textarea");
            return { same: !!det?.__first, entrances: window.__entrances, shifts: window.__shifts.length, first: window.__shifts[0], focused: document.activeElement === ta, text: ta?.value, caret: ta?.selectionStart };
          });
          assert.equal(s.entrances, 0, `${what}: the editor played its entrance again`);
          if (!moved) assert.equal(s.shifts, 0, `${what}: the editor moved or faded (${JSON.stringify(s.first)})`);
          assert.equal(s.focused, true, `${what}: the editor lost the focus`);
          assert.equal(s.text, "Use pnpm, never npm, in this repo here", `${what}: the text typed`);
          assert.equal(s.caret, 4, `${what}: the caret`);
          return s;
        };

        // two rounds of both polls with nothing new
        const reads = async (n) => {
          const s = st.semReads + n, d = st.distReads + n;
          while (st.semReads < s || st.distReads < d) await page.waitForTimeout(100);
          await page.waitForTimeout(150); // what a read draws, drawn
        };
        await reads(2);
        let s = await steady("polls with nothing new");
        assert.equal(s.same, true, "polls with nothing new: the editor was drawn anew");

        // what is new is said in its own row, the editor left alone
        st.sem = { ...st.sem, read: 2, reading: false };
        const was = await page.locator(`${view} .lib-memsem .sub`).textContent();
        await reads(1);
        await page.waitForFunction((was) => document.querySelector("#view-library .lib-memsem .sub")?.textContent !== was, was);
        // the row's new line may wrap, and move what is under it once
        s = await steady("search by meaning read them all", true);
        assert.equal(s.same, true, "a new reading: the editor was drawn anew");
        await page.evaluate(() => window.__rebase());

        // the run ends: the memories are read again, and the page is
        // drawn again with them, the editor open as it was
        st.dist = { ...st.dist, running: false };
        st.mem = memories();
        st.mem.memories.push({ id: "a3", text: "Answers in Chinese", kind: "preference", scope: "global", origin: "inferred", status: "active", created: at(3), updated: at(3) });
        await page.locator(`${view} .lib-mems .lib-mem[data-id="a3"]`).waitFor();
        await page.waitForTimeout(300);
        // a3 is newer, listed above it
        await steady("the run ended with a new memory", true);
        assert.equal(await page.locator(`${view} .lib-memdetail`).count(), 1);
        // a closed and reopened editor still comes in as before
        await row.evaluate((e) => e.click()); // at 420px the header can sit over it while the page settles
        await page.locator(`${view} .lib-memdetail`).waitFor({ state: "detached" });
        await row.evaluate((e) => e.click()); // at 420px the header can sit over it while the page settles
        await page.locator(`${view} .lib-memdetail`).waitFor();
        await page.waitForFunction(() => window.__entrances > 0);
        await ctx.close();
      });
    }
  });
}
