// Run with Node's test runner; see README.md.
// One maker for every Advanced fold (the owner, 11-27: 从根上避免这种垃圾布局).
// billion-context's was hand-rolled with the Library toolbar's .lib-more
// class and laid out as a row; each hand-rolled fold had its own summary
// CSS too. Now app.js's advFold makes them all, and its one rule set in
// app.css lays them out: no script under assets makes a <details> or a
// summary saying Advanced itself, and index.html's own folds carry
// adv-fold. adv-fold-layout measures them in the browser.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
function scripts(dir) {
  const out = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== "vendor" && e.name !== "node_modules") out.push(...scripts(p)); }
    else if (/\.(js|mjs)$/.test(e.name)) out.push(p);
  }
  return out;
}

test("every Advanced fold is made by advFold", () => {
  const bad = [];
  for (const f of scripts(assets)) {
    const src = fs.readFileSync(f, "utf8"), rel = path.relative(assets, f);
    // the maker's own body is the one place a fold's summary says Advanced
    const own = rel === "app.js" ? src.indexOf("function advFold(") : -1;
    const ownEnd = own >= 0 ? src.indexOf("\n}\n", own) : -1;
    const outside = (i) => !(own >= 0 && i > own && i < ownEnd);
    const line = (i) => src.slice(0, i).split("\n").length;
    for (const m of src.matchAll(/el\(\s*"summary"[^;\n]*t\(\s*"Advanced"\s*\)/g)) {
      if (outside(m.index)) bad.push(`${rel}:${line(m.index)}: a summary saying Advanced, not advFold's`);
    }
    // a hand-made <details> with Advanced in the lines that build it
    for (const m of src.matchAll(/(?:el\(\s*"details"|createElement\(\s*"details"\s*\))/g)) {
      if (!outside(m.index)) continue;
      const near = src.slice(m.index, m.index + 600);
      if (/t\(\s*"Advanced"\s*\)/.test(near) || /\badvanced\b/.test(near.split("\n")[0])) bad.push(`${rel}:${line(m.index)}: an Advanced fold made by hand`);
    }
    for (const m of src.matchAll(/el\(\s*"details"\s*,\s*"[^"]*\blib-more\b/g)) bad.push(`${rel}:${line(m.index)}: a fold given the toolbar's lib-more`);
  }
  const html = fs.readFileSync(path.join(assets, "index.html"), "utf8");
  for (const m of html.matchAll(/<details\b([^>]*)>\s*<summary[^>]*>([^<]*)</g)) {
    if (m[2].trim() === "Advanced" && !/class="[^"]*\badv-fold\b/.test(m[1])) bad.push(`index.html: <details${m[1]}> isn't an adv-fold`);
  }
  assert.deepEqual(bad, []);
});

test("one rule set lays out every fold's summary", () => {
  const css = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) { if (e.name !== "vendor") walk(p); } else if (e.name.endsWith(".css")) css.push(p);
    }
  };
  walk(assets);
  const bad = [];
  for (const f of css) {
    const src = fs.readFileSync(f, "utf8"), rel = path.relative(assets, f);
    // a chevron drawn by hand on an Advanced fold's summary, outside .adv-fold's set
    for (const m of src.matchAll(/^([^{}\n]*summary::before[^{}\n]*)\{/gm)) {
      const sel = m[1];
      if (/(?:prefs-more|ag-iso-more|lib-bili-more|\.more\b|proxy-advanced|key-bal-auth)/.test(sel) && !/^\s*\.adv-fold/.test(sel)) bad.push(`${rel}: ${sel.trim()}`);
    }
  }
  assert.deepEqual(bad, []);
});
