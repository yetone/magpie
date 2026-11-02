// Run with Node's test runner and Playwright on the module path; see README.md.
// Tao on Discord (#feedback): in Library → Skills a skill turned off on
// this computer (handoff, from mattpocock/skills) shows "Off on this
// computer" on a line under its name, but its row kept the height of a row
// without it, so the description and the GitHub path under it were cut off
// at the row's bottom. grill-me, beside it and on, showed all three lines.
// Every line of a row, the tag's too, sits inside the row's padding: with
// and without a source, on and off here, in every language the tag is in,
// in Chromium and WebKit, wide and narrow. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tao";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
// the repository and the path line as the screenshot shows them; the
// descriptions are stand-ins (the screenshot cuts handoff's off)
const gh = (name, description, off) => ({
  name, description, kind: "github", source: `https://github.com/mattpocock/skills/tree/HEAD/skills/${name}`,
  agents: ["claude", "codex"], ...(off ? { offHere: true } : {}),
});
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [],
  checkDue: false,
  skills: [
    gh("grill-me", "Interview the user relentlessly about a plan until every branch of the decision tree is resolved.", false),
    gh("handoff", "Write a handoff document so another agent can pick up the work where this session left off.", true),
    // one with no source: no path line, the tag's line still counted
    { name: "notes", description: "Keep notes for the session", kind: "folder", agents: ["claude"], offHere: true },
    { name: "plain", description: "A skill that is on", kind: "folder", agents: ["claude"] },
  ],
});

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the tag in every language it is in (plugins/library/i18n.js)
const tags = { en: "Off on this computer", zh: "本机已关闭", "zh-TW": "本機已關閉", ja: "このコンピュータでオフ", de: "Auf diesem Computer aus" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill off on this computer shows its description and path whole", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => { await browser.close(); });
    for (const width of [860, 400]) for (const lang of Object.keys(tags)) {
      await t.test(`${lang} at ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (n) => v.locator(".lib-skill", { has: page.locator(".name", { hasText: new RegExp("^" + n) }) });
        for (const n of ["grill-me", "handoff", "notes", "plain"]) await row(n).waitFor();
        assert.equal((await row("handoff").locator(".lib-tag.lib-offhere").textContent()).trim(), tags[lang]);
        assert.equal(await row("grill-me").locator(".lib-offhere").count(), 0);

        // each line of each row: inside the row's padding, not under its bottom
        const lines = (n) => row(n).evaluate((r) => {
          const cs = getComputedStyle(r), rb = r.getBoundingClientRect();
          const top = rb.top + parseFloat(cs.borderTopWidth) + parseFloat(cs.paddingTop);
          const bottom = rb.bottom - parseFloat(cs.borderBottomWidth) - parseFloat(cs.paddingBottom);
          return [...r.querySelectorAll(":scope > .who > *")].map((e) => {
            const b = e.getBoundingClientRect();
            return { line: e.className, top: b.top - top, over: b.bottom - bottom, h: b.height };
          });
        });
        for (const [n, want] of [["grill-me", ["name", "sub", "lib-src"]], ["handoff", ["name", "lib-hereline", "sub", "lib-src"]], ["notes", ["name", "lib-hereline", "sub"]], ["plain", ["name", "sub"]]]) {
          const got = await lines(n);
          assert.deepEqual(got.map((l) => l.line), want, `${n}'s lines`);
          // 2px of room: Linux's Japanese font sets a row without the tag
          // ~1.3px taller than the list counts (#4901);
          // the tag's missing line was ~11px over each edge
          for (const l of got) {
            assert(l.h > 0, `${n}'s ${l.line} has no height`);
            assert(l.top >= -2, `${n}'s ${l.line} starts above the row's padding (${l.top}px)`);
            assert(l.over <= 2, `${n}'s ${l.line} is cut off at the row's bottom by ${l.over.toFixed(1)}px at ${width}px`);
          }
        }
        // the path line reads whole: the text the reporter lost
        const src = row("handoff").locator(".lib-srclink");
        assert.equal((await src.textContent()).trim(), "mattpocock/skills/tree/HEAD/skills/handoff");
        assert(await src.isVisible());
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
