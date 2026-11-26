// Run with Node's test runner and Playwright on the module path; see README.md.
// #7396: a Usage window's figure is split on " · " and only the first two
// pieces were drawn, so a plugin display ("Online · G 100%/50% · C 100%/0%
// · A 35%") and a counted window whose own text already has a " · " lost
// every piece after the second. The hover title still had the whole
// string. Every piece is drawn, and a count of one or two pieces keeps
// the same spans it had (the count, then " ·", then the share). English
// and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const aside = "Online · G 100%/50% · C 100%/0% · A 35%";
const noted = "Ready · East · 3 left";

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// pieces a window's figure is drawn as: each but the last keeps the " ·"
// that the old two-piece split put on the count, with a space between
function pieces(text) {
  const parts = text.split(" · ");
  const out = [];
  for (let i = 0; i < parts.length; i++) {
    const more = parts.slice(i + 1).some(Boolean);
    if (!i || parts[i] || more) out.push(more ? parts[i] + " ·" : parts[i]);
  }
  return out;
}

const engines = process.env.BROWSER === "webkit" ? [["webkit", webkit]]
  : process.env.BROWSER === "chromium" ? [["chromium", chromium]]
  : [["chromium", chromium], ["webkit", webkit]];

for (const [name, engine] of engines) {
  for (const lang of ["en", "zh"]) {
    test(`${name} ${lang}: a window keeps every · segment`, async (t) => {
      const browser = await engine.launch();
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/");
      await page.waitForFunction(() => typeof quotaWindows === "function" && typeof quotaText === "function");
      const got = await page.evaluate(async ({ aside: asideText, noted: notedText }) => {
        const windows = [
          { name: "Account A", display: asideText, aside: true },
          { name: "Note", text: true, used: 0, display: notedText },
          { name: "Session", used: 35, display: "Online · G 100%/50%" },
          { name: "5 hours", used: 40 },
          { name: "Credits", used: 29.6, display: "710.99 / 2400", amount: 710.99, limit: 2400, unit: "credits" },
          { name: "Week", used: 12, historyMissing: true },
        ];
        const said = (w) => (w.historyMissing ? t("No readings in this cycle") : quotaText(w));
        const read = (w) => {
          const box = quotaWindows({ name: "Plugin", user: "a", windows: [w] });
          const n = box.querySelector(".quota-n");
          const track = box.querySelector(".quota-track");
          return {
            spans: [...n.querySelectorAll(":scope > span")].map((s) => s.textContent),
            nodes: [...n.childNodes].map((c) => (c.nodeType === 3 ? c.data : c.textContent)),
            text: n.textContent,
            title: n.title,
            disabled: n.disabled,
            track: track ? track.style.visibility || "shown" : "none",
            full: said(w),
          };
        };
        const rows = windows.map(read);
        const host = document.createElement("div");
        host.style.width = "480px";
        document.body.append(host);
        host.append(quotaWindows({ name: "Plugin", user: "a", windows }));
        await new Promise((r) => { let n = 6; const f = () => (--n ? requestAnimationFrame(f) : r()); requestAnimationFrame(f); });
        const laid = [...host.querySelectorAll(".quota")].map((q) => {
          const n = q.querySelector(".quota-n");
          return {
            text: n.textContent,
            stacked: n.closest(".quota-windows").classList.contains("stacked"),
            cut: n.scrollWidth > n.clientWidth + 1 || [...n.children].some((s) => s.scrollWidth > s.clientWidth + 1),
          };
        });
        return { rows, laid };
      }, { aside, noted });

      const [many, note, metered, one, two, missing] = got.rows;
      assert.equal(many.full, aside, "a display the reader hasn't a word for is shown as given");
      assert.deepEqual(many.spans, ["Online ·", "G 100%/50% ·", "C 100%/0% ·", "A 35%"]);
      assert.deepEqual(many.nodes, ["Online ·", " ", "G 100%/50% ·", " ", "C 100%/0% ·", " ", "A 35%"]);
      assert.equal(many.text, aside);
      assert.equal(many.track, "none", "an aside with no share draws no meter");
      assert.equal(many.disabled, false);
      assert.equal(many.title.split("\n")[0], aside);

      assert.equal(note.full, noted);
      assert.deepEqual(note.spans, ["Ready ·", "East ·", "3 left"]);
      assert.equal(note.text, noted);
      assert.equal(note.track, "none");

      assert.ok(metered.full.split(" · ").length >= 3, metered.full);
      assert.equal(metered.text, metered.full, "the share after a display's own · stays");
      assert.deepEqual(metered.spans, pieces(metered.full));
      assert.equal(metered.track, "shown");
      assert.equal(metered.title, metered.full + "\n" + metered.title.split("\n").slice(1).join("\n"));
      assert.ok(metered.title.split("\n")[1], "the toggle hint stays on the title");

      assert.equal(one.full.split(" · ").length, 1, one.full);
      assert.deepEqual(one.spans, [one.full]);
      assert.deepEqual(one.nodes, [one.full]);
      assert.equal(one.text, one.full);
      assert.equal(one.track, "shown");
      assert.equal(one.title, one.full + "\n" + one.title.split("\n")[1]);

      assert.equal(two.full.split(" · ").length, 2, two.full);
      assert.deepEqual(two.spans, pieces(two.full));
      assert.deepEqual(two.nodes, [two.spans[0], " ", two.spans[1]]);
      assert.equal(two.spans[0], two.full.split(" · ")[0] + " ·");
      assert.equal(two.spans[1], two.full.split(" · ")[1]);
      assert.equal(two.text, two.full);
      assert.equal(two.track, "shown");

      assert.equal(missing.spans.length, 1);
      assert.equal(missing.text, missing.full);
      assert.doesNotMatch(missing.text, /%|·/);
      assert.equal(missing.disabled, true);
      assert.equal(missing.title, missing.full, "a cycle with no readings has no toggle hint");
      assert.equal(missing.track, "hidden");

      assert.equal(got.laid.length, 6);
      for (const [i, row] of got.laid.entries()) {
        assert.equal(row.text, got.rows[i].text, `laid out ${i}`);
        assert.equal(row.cut, false, `${lang} ${row.text} is cut (stacked ${row.stacked})`);
      }
      assert.equal(got.laid[0].stacked, true, "a four-part display wraps rather than staying one clipped line");
      assert.deepEqual(errors, []);
    });
  }
}
