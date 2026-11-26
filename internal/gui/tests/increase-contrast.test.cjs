// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Appearance › Increase contrast (usrname1833 on Discord: the
// light theme is grey on white with hairline edges, hard to read with low
// vision, astigmatism or older eyes; in Sessions a project and its
// sessions look alike). In both engines, with the API faked, the page is
// given contrast.css as appearance.go's lookCSS gives it, and:
// - the row is beside the theme; On saves contrast "more" and the page
//   takes the server's stylesheet with no reload; Off saves "normal";
// - left unset, it follows the system's prefers-contrast, and the toggle
//   says so; turned off, it stays off while the system asks;
// - on, text reads at 4.5:1 and the edges of lists at 3:1 on the grounds
//   they are drawn on, in light and dark, in the window and the tray
//   panel, where magpie's own look gives grey on white (edges 1.2:1);
// - in Sessions a project's row is heavier, shaded, and its sessions sit
//   further in, in a lighter weight; projects are parted at 3:1;
// - every language, wide and at 420px: the row speaks it and nothing
//   scrolls sideways.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const langs = ["en", "zh", "zh-TW", "ja", "de"];
const NAME = { en: "Increase contrast", zh: "增加对比度", "zh-TW": "增加對比", ja: "コントラストを上げる", de: "Kontrast erhöhen" };

// appearance.go's lookCSS for a contrast setting: contrast.css as written
// when on, inside @media (prefers-contrast: more) when unset, none when off
const contrastCSS = require("node:fs").readFileSync(path.resolve(__dirname, "../contrast.css"), "utf8");
const lookCSS = (c) => (c === "more" ? contrastCSS : c === "normal" ? "" : "@media (prefers-contrast: more){\n" + contrastCSS + "}\n");

const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min) => ({
  agent: "claude", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  resume: `cd ${cwd} && claude --resume ${id}`, path: `~/.claude/projects/x/${id}.jsonl`, size: 1024, messages: 4, files: 1, deletable: true,
});
const sessions = [
  sess("a-1", "/home/me/work/app", "fix the login", 20),
  sess("a-2", "/home/me/work/app", "add tests", 40),
  sess("b-1", "/home/me/blog", "write the post", 300),
  sess("b-2", "/home/me/blog", "fix a typo", 400),
];

function serve(st, log) {
  const settings = () => ({ lang: "en", theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      const s = settings(), css = lookCSS(s.contrast);
      const boot = { lang: s.lang, theme: s.theme, web: false, look: "," + (s.contrast || "") };
      return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify(boot)};` + (css ? `(function(){var e=document.createElement("style");e.id="lookCSS";e.textContent=${JSON.stringify(css)};document.head.appendChild(e)})();` : "") });
    }
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        log.push(["settings", b]);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/appearance/css") {
      log.push(["css"]);
      return json({ css: lookCSS(st.contrast), key: "," + (st.contrast || "") });
    }
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "claude", count: sessions.length, deletable: true, name: "Claude Code", icon: "claude" }],
        agent: "claude", home: "/home/me", sessions, terminal: false, trash: [], trashDir: "~/.local/share/magpie/trash/sessions" });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", calls: [] } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return r.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));

async function open(browser, st, { width = 1000, contrast = "no-preference", where = "?view=settings" } = {}) {
  const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce", colorScheme: st.theme || "light", contrast });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], log = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(st, log));
  await page.goto("http://magpie.test/" + where);
  return { page, errors, log };
}

const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

// WCAG ratios as the page draws them. A colour comes back as rgb()/rgba()
// or, from a color-mix, as color(srgb r g b); a see-through one is laid
// over the ground it is drawn on.
const inPage = () => {
  window.__rgb = (s) => {
    let m = s.match(/^color\(srgb ([\d.e-]+) ([\d.e-]+) ([\d.e-]+)(?: \/ ([\d.]+))?\)$/);
    if (m) return [m[1] * 255, m[2] * 255, m[3] * 255, m[4] === undefined ? 1 : +m[4]];
    m = s.match(/[\d.]+/g).map(Number);
    return [m[0], m[1], m[2], m[3] ?? 1];
  };
  window.__lum = (c) => { const f = (v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]); };
  window.__over = (top, under) => [0, 1, 2].map((i) => top[i] * top[3] + under[i] * (1 - top[3]));
  window.__ratio = (a, b) => { const [x, y] = [__lum(a), __lum(b)].sort((p, q) => p - q); return (y + 0.05) / (x + 0.05); };
  // the theme's colours, as resolved inside body (the tray panel sets its own there)
  window.__tok = (k) => {
    const i = document.createElement("i");
    i.style.color = `var(--${k})`;
    document.body.append(i);
    const c = __rgb(getComputedStyle(i).color);
    i.remove();
    return c;
  };
};

// the least ratio of text and of edges over the grounds they are drawn on
const ratios = (page) => page.evaluate(() => {
  const grounds = ["bg", "card", "card-2", "pill", "pill-hover"].map((k) => [k, __tok(k)]);
  const bg = __tok("bg");
  const lowest = (k) => Math.min(...grounds.map(([, g]) => { const c = __tok(k); return __ratio(__over(c, __over(g, bg)), __over(g, bg)); }));
  const out = {};
  for (const k of ["fg", "fg-2", "muted", "faint", "ctl-fg"]) out[k] = lowest(k);
  out.line = Math.min(__ratio(__over(__tok("line"), __tok("card")), __tok("card")), __ratio(__over(__tok("line"), bg), bg));
  return out;
});

// a Settings list's drawn edge against the card inside it and the page outside
const listEdge = (page) => page.evaluate(() => {
  const list = document.querySelector("#view-settings .list");
  const cs = getComputedStyle(list), bg = __tok("bg");
  const card = __over(__rgb(cs.backgroundColor), bg), edge = __rgb(cs.borderTopColor);
  return Math.min(__ratio(__over(edge, card), card), __ratio(__over(edge, bg), bg));
});

const pressed = (page) => page.locator("#contrastSegs .opt.on").getAttribute("data-seg");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: On and Off are saved and drawn at once, and left unset it follows the system`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    {
      const { page, errors, log } = await open(browser, {});
      await page.evaluate(inPage);
      await page.locator("#contrastSegs .segs").waitFor();
      // beside the theme
      assert.equal(await page.evaluate(() => document.getElementById("themeSegs").closest(".row").nextElementSibling.id), "contrastRow");
      assert.equal(await pressed(page), "off");
      const was = await listEdge(page);
      assert.ok(was < 3, `magpie's own look already draws edges at ${was.toFixed(2)}:1`);
      await shot(page, `${engine}-contrast-settings-off`);

      await page.locator('#contrastSegs .opt[data-seg="on"]').click();
      // the sheet as written: unset, boot.js drew it already inside @media, so --line-2 alone is no sign of the click
      await page.waitForFunction(() => { const c = document.getElementById("lookCSS")?.textContent || ""; return c.includes("--line-2") && !c.startsWith("@media"); });
      assert.deepEqual(log.filter(([k]) => k === "settings").at(-1)[1].contrast, "more");
      assert.ok(log.some(([k]) => k === "css"), "the page didn't take the server's stylesheet");
      const on = await listEdge(page);
      assert.ok(on >= 3, `a list's edge is ${on.toFixed(2)}:1 with contrast on`);
      const r = await ratios(page);
      for (const k of ["fg", "fg-2", "muted", "faint", "ctl-fg"]) assert.ok(r[k] >= 4.5, `${k} reads at ${r[k].toFixed(2)}:1`);
      assert.ok(r.line >= 3, `--line is ${r.line.toFixed(2)}:1`);
      await shot(page, `${engine}-contrast-settings-on`);

      // Off is "normal": off from then on, whatever the system asks
      await page.locator('#contrastSegs .opt[data-seg="off"]').click();
      await page.waitForFunction(() => !document.getElementById("lookCSS")?.textContent);
      assert.equal(log.filter(([k]) => k === "settings").at(-1)[1].contrast, "normal");
      await page.emulateMedia({ contrast: "more" });
      assert.ok((await listEdge(page)) < 3, "turned off, the system's ask still drew it");
      assert.deepEqual(errors, []);
    }
    {
      // never set, with the system asking for more: drawn from the first paint, and the toggle says On
      const { page, errors, log } = await open(browser, {}, { contrast: "more" });
      await page.evaluate(inPage);
      await page.locator("#contrastSegs .segs").waitFor();
      assert.equal(await pressed(page), "on");
      assert.ok((await listEdge(page)) >= 3, "unset, the system's ask wasn't followed");
      assert.ok(!log.some(([k]) => k === "css"), "boot.js drew the look; the page needn't ask for it");
      assert.deepEqual(errors, []);
    }
  });

  for (const theme of ["light", "dark"]) {
    for (const where of ["?view=settings", "?mode=panel"]) {
      test(`${engine}: ${theme} ${where}: text at 4.5:1 and edges at 3:1`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        for (const contrast of ["normal", "more"]) {
          const { page, errors } = await open(browser, { theme, contrast }, { width: where === "?mode=panel" ? 360 : 1000, where });
          await page.waitForFunction((panel) => (panel ? document.body.classList.contains("panel") : !!document.querySelector("#view-settings .list")), where === "?mode=panel");
          await page.evaluate(inPage);
          const r = await ratios(page);
          if (contrast === "more") {
            for (const k of ["fg", "fg-2", "muted", "faint", "ctl-fg"]) assert.ok(r[k] >= 4.5, `${k} reads at ${r[k].toFixed(2)}:1`);
            assert.ok(r.line >= 3, `--line is ${r.line.toFixed(2)}:1`);
          } else {
            // magpie's own look is left as it was: its edges are hairlines
            assert.ok(r.line < 3, `magpie's own --line is ${r.line.toFixed(2)}:1`);
          }
          assert.deepEqual(errors, []);
        }
      });
    }
  }

  for (const theme of ["light", "dark"]) {
    test(`${engine}: ${theme}: in Sessions a project's row reads as the head of its sessions`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const look = {};
      for (const contrast of ["normal", "more"]) {
        const { page, errors } = await open(browser, { theme, contrast }, { where: "?view=providers" });
        await page.evaluate(() => show("sessions"));
        await page.locator("#view-sessions .sm-folders .row.sm-sess").first().waitFor();
        await page.evaluate(inPage);
        look[contrast] = await page.evaluate(() => {
          const view = document.getElementById("view-sessions");
          const folder = view.querySelector(".sm-folders .row.sm-folder"), s = view.querySelector(".sm-folders .row.sm-sess");
          const fname = folder.querySelector(".sm-fline .name"), sname = s.querySelector(".who > .name");
          const card = __over(__rgb(getComputedStyle(view.querySelector(".sm-folders").closest(".list") || view.querySelector(".sm-folders")).backgroundColor), __tok("bg"));
          const fbg = __over(__rgb(getComputedStyle(folder).backgroundColor), card), sbg = __over(__rgb(getComputedStyle(s).backgroundColor), card);
          const second = view.querySelector(".sm-folders > .sm-group + .sm-group");
          const edge = __over(__rgb(getComputedStyle(second).borderTopColor), card);
          return {
            weights: [+getComputedStyle(fname).fontWeight, +getComputedStyle(sname).fontWeight],
            indent: sname.getBoundingClientRect().left - fname.getBoundingClientRect().left,
            shade: __ratio(fbg, sbg),
            parted: __ratio(edge, card),
            sname: __ratio(__rgb(getComputedStyle(sname).color), sbg),
          };
        });
        await shot(page, `${engine}-${theme}-contrast-${contrast}-sessions`);
        assert.deepEqual(errors, []);
      }
      const on = look.more, off = look.normal;
      assert.ok(on.weights[0] >= 600 && on.weights[1] <= 400, `weights ${on.weights}`);
      assert.ok(on.indent >= off.indent + 8, `a session sits ${on.indent}px in, ${off.indent}px without`);
      assert.ok(on.shade >= 1.05 && off.shade < 1.05, `a project's row is ${on.shade.toFixed(3)}:1 off its sessions'`);
      assert.ok(on.parted >= 3, `projects are parted at ${on.parted.toFixed(2)}:1`);
      assert.ok(on.sname >= 4.5, `a session's title reads at ${on.sname.toFixed(2)}:1`);
    });
  }

  for (const lang of langs) {
    for (const width of [420, 1000]) {
      test(`${engine}: ${lang} at ${width}px: the row speaks the language and nothing scrolls sideways`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const { page, errors } = await open(browser, { lang, contrast: "more" }, { width });
        await page.locator("#contrastSegs .segs").waitFor();
        assert.equal((await page.locator("#contrastRow .name").textContent()).trim(), NAME[lang]);
        if (lang !== "en") {
          const missing = await page.evaluate((lang) => ["Increase contrast", "Darker text, clearer edges and selection, as with the system's Increase Contrast"].filter((k) => !I18N[lang][k]), lang);
          assert.deepEqual(missing, []);
        }
        const fit = await page.evaluate(() => {
          const row = document.getElementById("contrastRow"), r = row.getBoundingClientRect(), out = [];
          if (row.scrollWidth > row.clientWidth + 1) out.push("the row scrolls");
          for (const b of row.querySelectorAll("button")) {
            const x = b.getBoundingClientRect();
            if (x.right > r.right + 1 || x.left < r.left - 1) out.push(b.textContent + " outside");
          }
          return { out, page: document.documentElement.scrollWidth <= innerWidth };
        });
        assert.deepEqual(fit.out, []);
        assert.ok(fit.page, "Settings scrolls sideways");
        await page.evaluate(() => show("sessions"));
        await page.locator("#view-sessions .row.sm-sess").first().waitFor();
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "Sessions scrolls sideways");
        const rows = await page.evaluate(() => [...document.querySelectorAll("#view-sessions .row.sm-sess, #view-sessions .row.sm-folder")].filter((r) => r.scrollWidth > r.clientWidth + 1).length);
        assert.equal(rows, 0, "a Sessions row scrolls");
        await shot(page, `${engine}-${lang}-${width}-contrast-sessions`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
