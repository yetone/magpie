// Run with Node's test runner and Playwright on the module path; see README.md.
// A page's own search box and the list (or the empty box) right under it
// are one space apart on every page that has one: the owner, 11-21, on
// Library › Subagents, "这里的间距有点点小，稍微再大一点点 … 这种布局有
// 特别多，都要统一修改间距". The space is app.css's --find-gap (10px);
// Library's add-from-a-source cards had 4px. Checked empty and with rows,
// Chromium and WebKit, wide and narrow, two languages; the API is faked.
// Not here, on purpose: searches in the title bar (Plugins, Settings, the
// Providers Add sheet's tools), Routing groups' filter in its section
// heading (.row-head, as every heading), Usage › Requests' search (a note
// line sits under it), Library › Skills' source card (its list's own
// heading comes first), and boxes in menus, pickers and sheets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const GAP = 10;
const HOME = "/Users/cheng";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = (full) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  skills: full ? [{ name: "pdf", description: "Reads PDFs", kind: "github", agents: ["claude"], source: "https://github.com/owner/repo/tree/main/pdf" }] : [],
  hooks: [], hookAgents: [],
  subagents: full ? [{ name: "reviewer", description: "Reviews a change", agents: ["claude"] }] : [],
  subagentAgents: [{ id: "claude", dir: `${HOME}/.claude/agents`, skills: true, mcp: true }], subagentModels: [],
});
const prov = (id) => ({
  id, name: id, icon: "generic", host: id + ".example.com", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: id + "-chat", name: id + " Chat", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…" + id }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});
const now = () => new Date().toISOString();
const sessions = [0, 1, 2].map((i) => ({ agent: "pi", id: "s" + i, cwd: "", title: "Session " + i, gateway: true, start: now(), last: now(), models: [{ model: "m" }], size: 2048, deletable: true }));
const masking = {
  days: [{ day: "2026-11-20", requests: 1, n: 1, distinct: 1 }], requests: 1, n: 2, distinct: 2, blocked: 0, hours: [],
  kinds: [{ name: "EMAIL", n: 1, distinct: 1 }, { name: "API_KEY", n: 1, distinct: 1 }], by: { rule: 2 },
  agents: [{ name: "claude", n: 2, distinct: 2 }], providers: [{ name: "anthropic", n: 2, distinct: 2 }], checked: [], values: [], since: now(),
};

const serve = (lang, full) => async (route) => {
  const url = new URL(route.request().url()), p = url.pathname;
  const json = (data) => route.fulfill({ json: data });
  if (p === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
  if (p === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (p === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (p === "/api/library") return json(lib(full));
  if (p === "/api/providers") return json({ providers: ["openai", "anthropic", "deepseek"].map(prov), presets: [], excluded: [], gateway: { running: true, window: true } });
  if (p === "/api/library/market/servers") return json({ items: full ? [{ id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true }] : [] });
  if (p === "/api/library/market/skills") return json({ items: full ? [{ id: "acme/skills/pdf", name: "pdf", source: "acme/skills", skillId: "pdf", installs: 1200, description: "PDFs" }] : [] });
  if (p === "/api/plugins") return json({ plugins: [] });
  if (p === "/api/groups") return json({ groups: [], models: [] });
  if (p === "/api/usage/quotas") return json([]);
  if (p === "/api/sessions/manage") return json({ agents: [{ agent: "pi", count: 3, deletable: true, name: "Pi", icon: "pi" }], agent: "pi", sessions, terminal: false, trash: [], trashDir: "~/trash", recording: true, recorded: true });
  if (p === "/api/masking/summary") return json(masking);
  if (p === "/api/masking/records") return json({ records: [], more: false });
  if (p.startsWith("/api/")) return json({});
  if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
  const file = path.join(assets, p === "/" ? "index.html" : p);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

// the pages: where to go, the search bar, the states it is checked in, and
// what shows its list has come. Discover's no-results line is a message,
// not a list, and is left out.
const libTab = (tab) => `try { localStorage.setItem("magpie.libTab", ${JSON.stringify(tab)}); } catch {}`;
const PAGES = [
  { name: "Library › Subagents", view: "library", init: libTab("subagents"), bar: ".lib-safind", states: [false, true] },
  { name: "Library › Claude plugins", view: "library", init: libTab("plugins"), bar: ".lib-plfind", states: [false] },
  { name: "Library › Skills Discover", view: "library", init: libTab("skills"), bar: ".mk-find", states: [true], ready: ".mk-card" },
  { name: "Library › MCP Discover", view: "library", init: libTab("mcp"), bar: ".mk-find", states: [true], ready: ".mk-card" },
  { name: "Providers", view: "providers", bar: "#pvHead", states: [true] },
  { name: "Sessions", view: "sessions", bar: ".sm-head", states: [true] },
  { name: "Masking", view: "masking", bar: ".msk-bar", states: [true] },
];

// the space from the lowest thing drawn in the bar to the first thing drawn
// under it. Drawn: a box with a border or a background, an icon, or a line
// of text (its own glyph box, not its element's); a field's room for a
// finger that draws nothing is not.
const gapUnder = (bar) => bar.evaluate((b) => {
  const shown = (e) => { const r = e.getBoundingClientRect(), cs = getComputedStyle(e); return r.width > 0 && r.height > 0 && cs.visibility !== "hidden"; };
  const rects = (root) => {
    const out = [];
    for (const e of [root, ...root.querySelectorAll("*")]) {
      if (!shown(e)) continue;
      const cs = getComputedStyle(e);
      if (parseFloat(cs.borderTopWidth) > 0 || parseFloat(cs.borderBottomWidth) > 0 || !/^(rgba\(0, 0, 0, 0\)|transparent)$/.test(cs.backgroundColor) || /^(IMG|svg)$/.test(e.tagName)) out.push(e.getBoundingClientRect());
      for (const n of e.childNodes) {
        if (n.nodeType !== 3 || !n.textContent.trim()) continue;
        const r = document.createRange();
        r.selectNodeContents(n);
        out.push(...[...r.getClientRects()].filter((x) => x.height > 0));
      }
    }
    return out;
  };
  let next = b.nextElementSibling;
  while (next && !shown(next)) next = next.nextElementSibling;
  if (!next) return { error: "nothing under it" };
  const top = Math.min(...rects(next).map((r) => r.top));
  const bottom = Math.max(...rects(b).map((r) => r.bottom));
  const cls = next.className && typeof next.className === "string" ? "." + next.className.trim().split(/\s+/).join(".") : "";
  return { gap: top - bottom, next: next.tagName.toLowerCase() + (next.id ? "#" + next.id : "") + cls };
});

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a search box is ${GAP}px over its list on every page`, async (t) => {
        const browser = await (engine === "webkit" ? webkit : chromium).launch();
        t.after(() => browser.close());
        const wrong = [];
        for (const pg of PAGES) {
          for (const full of pg.states) {
            const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
            try {
              if (pg.init) await ctx.addInitScript(pg.init);
              const page = await ctx.newPage();
              page.setDefaultTimeout(10000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", serve(lang, full));
              await page.goto(`http://magpie.test/?view=${pg.view}`);
              const bar = page.locator(`${pg.bar}:visible`).first();
              await bar.waitFor();
              // the rows (or the empty box) drawn under it
              await page.waitForFunction((sel) => {
                const b = [...document.querySelectorAll(sel)].find((e) => e.getClientRects().length);
                let n = b?.nextElementSibling;
                while (n && !n.getClientRects().length) n = n.nextElementSibling;
                return n && !n.querySelector(".skeleton, .lib-skel") && !n.matches(".skeleton, .lib-skel");
              }, pg.bar);
              if (pg.ready) await page.locator(`${pg.ready}:visible`).first().waitFor();
              // cards rise into place: measured where they come to rest
              await page.evaluate(() => Promise.all([document.fonts.ready, ...document.getAnimations()
                .filter((a) => a.effect?.getTiming().iterations !== Infinity).map((a) => a.finished.catch(() => {}))]));
              const got = await gapUnder(bar);
              const where = `${pg.name} (${full ? "with rows" : "empty"})`;
              if (got.error) wrong.push(`${where}: ${got.error}`);
              else if (Math.abs(got.gap - GAP) > 0.5) wrong.push(`${where}: ${got.gap.toFixed(2)}px to ${got.next}`);
              assert.deepEqual(errors, [], `${where}: page errors`);
            } finally {
              await ctx.close();
            }
          }
        }
        assert.deepEqual(wrong, [], `not ${GAP}px:\n${wrong.join("\n")}`);
      });
    }
  }
}
